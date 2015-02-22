package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/types"

	"github.com/hashicorp/raft"
)

var (
	GetMessageRequests    = make(map[string]GetMessageStats)
	getMessagesRequestsMu sync.Mutex

	// To avoid setting up a new proxy on every request, we cache the proxies
	// for each node (since the current leader might change abruptly).
	nodeProxies   = make(map[string]*httputil.ReverseProxy)
	nodeProxiesMu sync.RWMutex
)

type GetMessageStats struct {
	Session types.RobustId
	Nick    string
	Started time.Time
}

func (stats GetMessageStats) StartedAndRelative() string {
	return stats.Started.Format("2006-01-02 15:04:05 -07:00") + " (" +
		time.Now().Round(time.Second).Sub(stats.Started.Round(time.Second)).String() + " ago)"
}

type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error {
	return nil
}

func maybeProxyToLeader(w http.ResponseWriter, r *http.Request, body io.ReadCloser) {
	leader := node.Leader()
	if leader == nil {
		http.Error(w, fmt.Sprintf("No leader known. Please try another server."),
			http.StatusInternalServerError)
		return
	}

	nodeProxiesMu.RLock()
	p, ok := nodeProxies[leader.String()]
	nodeProxiesMu.RUnlock()

	if !ok {
		u, err := url.Parse("https://" + leader.String())
		if err != nil {
			http.Error(w, fmt.Sprintf("url.Parse(): %v", err), http.StatusInternalServerError)
			return
		}
		p = httputil.NewSingleHostReverseProxy(u)
		p.Transport = robusthttp.Transport()

		// Races are okay, i.e. overwriting the proxy a different goroutine set up.
		nodeProxiesMu.Lock()
		nodeProxies[leader.String()] = p
		nodeProxiesMu.Unlock()
	}

	location := *r.URL
	location.Host = leader.String()
	w.Header().Set("Content-Location", location.String())
	log.Printf("Proxying request (%q) to leader %q\n", r.URL.Path, leader.String())
	r.Body = body
	p.ServeHTTP(w, r)
}

func session(r *http.Request, ps httprouter.Params) (types.RobustId, error) {
	var sessionid types.RobustId

	id, err := strconv.ParseInt(ps[0].Value, 0, 64)
	if err != nil {
		return sessionid, fmt.Errorf("invalid session: %v", err)
	}

	header := r.Header.Get("X-Session-Auth")
	if header == "" {
		return sessionid, fmt.Errorf("no X-Session-Auth header set")
	}

	auth, err := ircServer.GetAuth(types.RobustId{Id: id})
	if err != nil {
		return sessionid, err
	}
	if header != auth {
		return sessionid, fmt.Errorf("invalid X-Session-Auth header")
	}

	sessionid.Id = id

	return sessionid, nil
}

func sessionOrProxy(w http.ResponseWriter, r *http.Request, ps httprouter.Params) (types.RobustId, error) {
	sessionid, err := session(r, ps)
	if err == ircserver.ErrSessionNotYetSeen && node.State() != raft.Leader {
		// The session might exist on the leader, so we must proxy.
		maybeProxyToLeader(w, r, r.Body)
		return sessionid, err
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
	}
	return sessionid, err
}

// handlePostMessage is called by the robustirc-brigde whenever a message should be
// posted. The handler blocks until either the data was written or an error
// occurred. If successful, it returns the unique id of the message.
func handlePostMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	session, err := sessionOrProxy(w, r, ps)
	if err != nil {
		return
	}

	t := ircServer.GetLastActivity(session).Add(*postMessageCooloff)
	time.Sleep(t.Sub(time.Now()))

	type postMessageRequest struct {
		Data            string
		ClientMessageId uint64
	}

	var req postMessageRequest

	// We limit the amount of bytes read to 1024 to prevent reading overly long
	// requests in the first place. The IRC line length limit is 512 bytes, so
	// with 1024 bytes we have plenty of headroom to encode 512 bytes in JSON.
	//
	// We save a copy of the request in case we need to proxy it to the leader.
	var body bytes.Buffer
	rd := io.TeeReader(http.MaxBytesReader(w, r.Body, 1024), &body)
	if err := json.NewDecoder(rd).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// If we have already seen this message, we just reply with a canned response.
	if id, reply := ircServer.LastPostMessage(session); id == req.ClientMessageId && reply != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
		return
	}

	msg := types.NewRobustMessage(types.RobustIRCFromClient, session, req.Data)
	msg.ClientMessageId = req.ClientMessageId
	msgbytes, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not store message, cannot encode it as JSON: %v", err),
			http.StatusBadRequest)
		return
	}

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}

func handleJoin(w http.ResponseWriter, r *http.Request) {
	log.Println("Join request from", r.RemoteAddr)
	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, r.Body)
		return
	}

	type joinRequest struct {
		Addr string
	}
	var req joinRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(w, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Adding peer %q to the network.\n", req.Addr)

	if err := node.AddPeer(&dnsAddr{req.Addr}).Error(); err != nil && err != raft.ErrKnownPeer {
		log.Println("Could not add peer:", err)
		http.Error(w, "Could not add peer", 500)
		return
	}
}

func handleSnapshot(res http.ResponseWriter, req *http.Request) {
	log.Printf("snapshotting()\n")
	node.Snapshot()
	log.Println("snapshotted")
}

func handleGetMessages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Avoid sessionOrProxy() because GetMessages can be answered on any raft
	// node, it’s a read-only request.
	session, err := session(r, ps)
	if err != nil {
		if err == ircserver.ErrSessionNotYetSeen {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	remoteAddr := r.RemoteAddr
	getMessagesRequestsMu.Lock()
	GetMessageRequests[remoteAddr] = GetMessageStats{
		Session: session,
		Nick:    ircServer.GetNick(session),
		Started: time.Now(),
	}
	getMessagesRequestsMu.Unlock()
	defer func() {
		getMessagesRequestsMu.Lock()
		delete(GetMessageRequests, remoteAddr)
		getMessagesRequestsMu.Unlock()
	}()

	lastSeen := ircServer.GetStartId(session)
	lastSeenStr := r.FormValue("lastseen")
	if lastSeenStr != "0.0" {
		parts := strings.Split(lastSeenStr, ".")
		if len(parts) != 2 {
			log.Printf("cannot parse %q\n", lastSeenStr)
			http.Error(w, fmt.Sprintf("Malformed lastseen value (%q)", lastSeenStr),
				http.StatusInternalServerError)
			return
		}
		first, err := strconv.ParseInt(parts[0], 0, 64)
		if err != nil {
			log.Printf("cannot parse %q\n", lastSeenStr)
			http.Error(w, fmt.Sprintf("Malformed lastseen value (%q)", lastSeenStr),
				http.StatusInternalServerError)
			return
		}
		last, err := strconv.ParseInt(parts[1], 0, 64)
		if err != nil {
			log.Printf("cannot parse %q\n", lastSeenStr)
			http.Error(w, fmt.Sprintf("Malformed lastseen value (%q)", lastSeenStr),
				http.StatusInternalServerError)
			return
		}
		lastSeen = types.RobustId{
			Id:    first,
			Reply: last,
		}
		log.Printf("Trying to resume at %v\n", lastSeen)
	}

	enc := json.NewEncoder(w)
	var msgcopy types.RobustMessage
	flushTimer := time.NewTimer(1 * time.Second)
	flushTimer.Stop()
	var lastFlush time.Time
	willFlush := false
	msgschan := make(chan []*types.RobustMessage)
	done := make(chan bool, 1)
	go func() {
		var msgs []*types.RobustMessage
		for {
			select {
			case <-done:
				close(msgschan)
				return
			default:
			}
			msgs = ircServer.GetNext(lastSeen)
			lastSeen = msgs[0].Id
			msgschan <- msgs
		}
	}()
	defer func() {
		done <- true
		for _ = range msgschan {
		}
	}()
	for {
		select {
		case msgs := <-msgschan:
			if _, err := ircServer.GetSession(session); err != nil {
				// Session was deleted in the meanwhile, abort this request.
				return
			}

			for _, msg := range msgs {
				if msg.Type != types.RobustPing && !msg.InterestingFor[session.Id] {
					continue
				}

				// Remove the ClientMessageId before sending, just in case it contains
				// sensitive information (e.g. the random values leaking state of the
				// PRNG).
				msgcopy = *msg
				msgcopy.ClientMessageId = 0

				if err := enc.Encode(&msgcopy); err != nil {
					log.Printf("Error encoding JSON: %v\n", err)
					return
				}
			}

			if time.Since(lastFlush) > 100*time.Millisecond {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				lastFlush = time.Now()
			} else if !willFlush {
				// Delay flushing by 10ms to avoid flushing too often, which
				// results in lots of write() syscall.
				flushTimer.Reset(10 * time.Millisecond)
				willFlush = true
			}

		case <-flushTimer.C:
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			willFlush = false
			lastFlush = time.Now()
		}
	}
}

func handleCreateSession(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	if ps[0].Value != "session" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	b := make([]byte, 128)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, fmt.Sprintf("Cannot generate SessionAuth cookie: %v", err), http.StatusInternalServerError)
		return
	}
	sessionauth := fmt.Sprintf("%x", b)
	msg := types.NewRobustMessage(types.RobustCreateSession, types.RobustId{}, sessionauth)
	// Cannot fail, no user input.
	msgbytes, _ := json.Marshal(msg)

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{bytes.NewBuffer(nil)})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}

	sessionid := fmt.Sprintf("0x%x", msg.Id.Id)

	w.Header().Set("Content-Type", "application/json")

	type createSessionReply struct {
		Sessionid   string
		Sessionauth string
		Prefix      string
	}

	if err := json.NewEncoder(w).Encode(createSessionReply{sessionid, sessionauth, *network}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	session, err := sessionOrProxy(w, r, ps)
	if err != nil {
		return
	}

	type deleteSessionRequest struct {
		Quitmessage string
	}

	var req deleteSessionRequest
	var body bytes.Buffer
	rd := io.TeeReader(r.Body, &body)
	if err := json.NewDecoder(rd).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Could not decode request: %v", err), http.StatusInternalServerError)
		return
	}

	msg := types.NewRobustMessage(types.RobustDeleteSession, session, req.Quitmessage)
	// Cannot fail, no user input.
	msgbytes, _ := json.Marshal(msg)

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}

func handleLeader(w http.ResponseWriter, r *http.Request) {
	if leader := node.Leader(); leader != nil {
		w.Write([]byte(leader.String()))
	}
}

func handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("deletestate") == "yes" {
		f, err := os.Create(filepath.Join(*raftDir, "deletestate"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close()
	}
	log.Fatalf("Exiting because %v triggered /quit", r.RemoteAddr)
}

// handleCanaryLog streams the entire input log and the corresponding output
// messages, so that a new robustirc version can be run in canary mode (process
// all the messages of a real IRC network and display any differences).
func handleCanaryLog(w http.ResponseWriter, r *http.Request) {
	// TODO(secure): is it okay to compact during handleCanaryLog or should we introduce a mutex?
	first, err := ircStore.FirstIndex()
	if err != nil {
		return
	}

	last, err := ircStore.LastIndex()
	if err != nil {
		return
	}

	type canaryMessage struct {
		Index  uint64
		Input  *types.RobustMessage
		Output []*types.RobustMessage
	}

	encoder := json.NewEncoder(w)

	// Useful for the client to implement a progress indicator.
	w.Header().Set("X-RobustIRC-Canary-Messages", fmt.Sprintf("%d", last-first))
	// Necessary for the client to avoid differences in the 001 message.
	w.Header().Set("X-RobustIRC-Canary-ServerCreation", fmt.Sprintf("%d", ircServer.ServerCreation.UnixNano()))

	for i := first; i <= last; i++ {
		var elog raft.Log
		if err := ircStore.GetLog(i, &elog); err != nil {
			continue
		}
		if elog.Type != raft.LogCommand {
			continue
		}

		msg := types.NewRobustMessageFromBytes(elog.Data)
		output, _ := ircServer.Get(msg.Id)

		if err := encoder.Encode(canaryMessage{Index: i, Input: &msg, Output: output}); err != nil {
			log.Printf("Aborting handleCanaryLog: %v\n", err)
			return
		}
	}
}
