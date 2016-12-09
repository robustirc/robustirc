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

	"golang.org/x/net/context"

	"github.com/BurntSushi/toml"
	"github.com/julienschmidt/httprouter"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/types"
	"github.com/stapelberg/glog"

	"github.com/hashicorp/raft"
)

var (
	// getMessagesRequests contains information about each GetMessages request
	// to be exposed on the HTTP status handler.
	getMessagesRequests   = make(map[string]GetMessagesStats)
	getMessagesRequestsMu sync.RWMutex

	// applyMu guards calls to raft.Apply(). We need to lock them because
	// otherwise we cannot guarantee that multiple goroutines will write
	// strictly monotonically increasing timestamps.
	applyMu sync.Mutex

	// To avoid setting up a new proxy on every request, we cache the proxies
	// for each node (since the current leader might change abruptly).
	nodeProxies   = make(map[string]*httputil.ReverseProxy)
	nodeProxiesMu sync.RWMutex

	// lastContact stores either node.LastContact() for non-leaders or
	// time.Now() for leaders.
	lastContact = time.Now()
)

// GetMessageStats encapsulates information about a GetMessages request.
type GetMessagesStats struct {
	Session       types.RobustId
	Nick          string
	Started       time.Time
	UserAgent     string
	ForwardedFor  string
	TrustedBridge string
}

func (stats GetMessagesStats) NickWithFallback() string {
	if stats.Nick != "" {
		return stats.Nick
	}
	if session, err := ircServer.GetSession(stats.Session); err == nil {
		return session.Nick
	}
	return ""
}

// StartedAndRelative converts |stats.Started| into a human-readable formatted
// time, followed by a relative time specification.
func (stats GetMessagesStats) StartedAndRelative() string {
	return stats.Started.Format("2006-01-02 15:04:05 -07:00") + " (" +
		time.Now().Round(time.Second).Sub(stats.Started.Round(time.Second)).String() + " ago)"
}

type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error {
	return nil
}

func getNodeProxy(leader string) (*httputil.ReverseProxy, bool) {
	nodeProxiesMu.RLock()
	defer nodeProxiesMu.RUnlock()
	p, ok := nodeProxies[leader]
	return p, ok
}

func setNodeProxy(leader string, proxy *httputil.ReverseProxy) {
	nodeProxiesMu.Lock()
	defer nodeProxiesMu.Unlock()
	nodeProxies[leader] = proxy
}

func maybeProxyToLeader(w http.ResponseWriter, r *http.Request, body io.ReadCloser) {
	leader := node.Leader()
	if leader == "" {
		http.Error(w, fmt.Sprintf("No leader known. Please try another server."),
			http.StatusInternalServerError)
		return
	}

	p, ok := getNodeProxy(leader)
	if !ok {
		u, err := url.Parse("https://" + leader)
		if err != nil {
			http.Error(w, fmt.Sprintf("url.Parse(): %v", err), http.StatusInternalServerError)
			return
		}
		p = httputil.NewSingleHostReverseProxy(u)
		p.Transport = robusthttp.Transport(true)

		// Races are okay, i.e. overwriting the proxy a different goroutine set up.
		setNodeProxy(leader, p)
	}

	location := *r.URL
	location.Host = leader
	w.Header().Set("Content-Location", location.String())
	log.Printf("Proxying request (%q) to leader %q\n", r.URL.Path, leader)
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

// handlePostMessage is called by the robustirc-bridge whenever a message should be
// posted. The handler blocks until either the data was written or an error
// occurred. If successful, it returns the unique id of the message.
func handlePostMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	session, err := sessionOrProxy(w, r, ps)
	if err != nil {
		return
	}

	// Don’t throttle server-to-server connections (services)
	until := ircServer.ThrottleUntil(session)
	time.Sleep(until.Sub(time.Now()))

	type postMessageRequest struct {
		Data            string
		ClientMessageId uint64
	}

	var req postMessageRequest

	// We limit the amount of bytes read to 2048 to prevent reading overly long
	// requests in the first place. The IRC line length limit is 512 bytes, so
	// with 2048 bytes we have plenty of headroom to encode 512 bytes in JSON:
	// The highest unicode code point is 0x10FFFF¹, which is expressed with
	// 4 bytes when using UTF-8², but gets blown up to 12 bytes when escaped in
	// JSON³. Hence, the JSON representation of a 512 byte IRC message has an
	// upper bound of 3x512 = 1536 bytes. We use 2048 bytes to have enough
	// space for encoding the struct field names etc.
	// ① http://unicode.org/glossary/#code_point
	// ② http://tools.ietf.org/html/rfc3629#section-3
	// ③ https://tools.ietf.org/html/rfc7159#section-7
	//
	// We save a copy of the request in case we need to proxy it to the leader.
	var body bytes.Buffer
	rd := io.TeeReader(http.MaxBytesReader(w, r.Body, 2048), &body)
	if err := json.NewDecoder(rd).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// If we have already seen this message, we just reply with a canned response.
	if ircServer.LastPostMessage(session) == req.ClientMessageId {
		return
	}

	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	msg := &types.RobustMessage{
		Session:         session,
		Type:            types.RobustIRCFromClient,
		Data:            req.Data,
		ClientMessageId: req.ClientMessageId,
	}
	if err := applyMessageWait(msg, 10*time.Second); err != nil {
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

	if err := node.AddPeer(req.Addr).Error(); err != nil && err != raft.ErrKnownPeer {
		log.Println("Could not add peer:", err)
		http.Error(w, "Could not add peer", http.StatusInternalServerError)
		return
	}
}

func handlePart(w http.ResponseWriter, r *http.Request) {
	log.Println("Part request from", r.RemoteAddr)
	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, r.Body)
		return
	}

	type partRequest struct {
		Addr string
	}
	var req partRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(w, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Removing peer %q from the network.\n", req.Addr)

	if err := node.RemovePeer(req.Addr).Error(); err != nil && err != raft.ErrKnownPeer {
		log.Println("Could not remove peer:", err)
		http.Error(w, "Could not remove peer", http.StatusInternalServerError)
		return
	}
}

func handleSnapshot(res http.ResponseWriter, req *http.Request) {
	log.Printf("snapshotting()\n")
	node.Snapshot()
	log.Println("snapshotted")
}

func updateLastContact() {
	// node.LastContact() is only updated when we receive heartbeats from the
	// leader, i.e. only when we are a follower.
	if node.State() == raft.Follower && !node.LastContact().IsZero() {
		lastContact = node.LastContact()
	} else if node.State() == raft.Leader {
		lastContact = time.Now()
	}
}

func pingMessage() *types.RobustMessage {
	peers, err := peerStore.Peers()
	if err != nil {
		log.Fatalf("Could not get peers: %v (Peer file corrupted on disk?)", err)
	}
	return &types.RobustMessage{
		Type:    types.RobustPing,
		Servers: peers,
	}
}

func setGetMessagesRequests(remoteAddr string, stats GetMessagesStats) {
	getMessagesRequestsMu.Lock()
	defer getMessagesRequestsMu.Unlock()
	getMessagesRequests[remoteAddr] = stats
}

func deleteGetMessagesRequests(remoteAddr string) {
	getMessagesRequestsMu.Lock()
	defer getMessagesRequestsMu.Unlock()
	delete(getMessagesRequests, remoteAddr)
}

func copyGetMessagesRequests() map[string]GetMessagesStats {
	result := make(map[string]GetMessagesStats)
	getMessagesRequestsMu.RLock()
	defer getMessagesRequestsMu.RUnlock()
	for key, value := range getMessagesRequests {
		result[key] = value
	}
	return result
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
	setGetMessagesRequests(remoteAddr, GetMessagesStats{
		Session:       session,
		Nick:          ircServer.GetNick(session),
		Started:       time.Now(),
		UserAgent:     r.Header.Get("User-Agent"),
		ForwardedFor:  r.Header.Get("X-Forwarded-For"),
		TrustedBridge: ircServer.TrustedBridge(r.Header.Get("X-Bridge-Auth")),
	})
	defer deleteGetMessagesRequests(remoteAddr)

	lastSeen := ircServer.GetStartId(session)
	lastSeenStr := r.FormValue("lastseen")
	if lastSeenStr != "0.0" && lastSeenStr != "" {
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
	flushTimer := time.NewTimer(1 * time.Second)
	flushTimer.Stop()
	var lastFlush time.Time
	willFlush := false
	msgschan := make(chan []*types.RobustMessage)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		var msgs []*types.RobustMessage
		pingDone := make(chan bool)
		go func() {
			pingTicker := time.NewTicker(pingInterval)
			for {
				select {
				case <-pingTicker.C:
					msgschan <- []*types.RobustMessage{pingMessage()}
				case <-pingDone:
					pingTicker.Stop()
					return
				}
			}
		}()

		// With the following output messages stored for a session:
		// Id                  Reply
		// 1431542836610113945.1
		// 1431542836610113945.2     |lastSeen|
		// 1431542836610113945.3
		// 1431542836691955391.1
		// …when resuming, GetNext(1431542836610113945.2) will return
		// 1431542836691955391.*, skipping the remaining messages with
		// Id=1431542836610113945.
		// Hence, we need to Get(1431542836610113945.2) to send
		// 1431542836610113945.3 and following to the client.
		if msgs, ok := ircServer.Get(lastSeen); ok && int(lastSeen.Reply) < len(msgs) {
			msgschan <- msgs[lastSeen.Reply:]
		}

		for {
			if ctx.Err() != nil {
				pingDone <- true
				close(msgschan)
				return
			}
			msgs = ircServer.GetNext(ctx, lastSeen)
			if len(msgs) == 0 {
				continue
			}
			// This check prevents replaying old messages in the scenario where
			// the client has seen newer messages than the server, for example
			// because the server is currently recovering from a snapshot after
			// being restarted, or because the server’s network connection to
			// the rest of the network is currently slow.
			if msgs[0].Id.Id < lastSeen.Id {
				glog.Warningf("lastSeen (%d) more recent than GetNext() result %d\n", lastSeen.Id, msgs[0].Id.Id)
				glog.Warningf("This should only happen while the server is recovering from a snapshot\n")
				glog.Warningf("The message in question is %v\n", msgs[0])
				// Prevent busylooping while new messages are applied.
				time.Sleep(250 * time.Millisecond)
				continue
			}

			lastSeen = msgs[0].Id
			msgschan <- msgs
		}
	}()
	defer func() {
		cancel()
		ircServer.InterruptGetNext()
		for _ = range msgschan {
		}
	}()
	for {
		select {
		case msgs := <-msgschan:
			for _, msg := range msgs {
				if msg.Type != types.RobustPing && !msg.InterestingFor[session.Id] {
					continue
				}

				if err := enc.Encode(msg); err != nil {
					log.Printf("Error encoding JSON: %v\n", err)
					return
				}
			}

			if _, err := ircServer.GetSession(session); err != nil {
				// Session was deleted in the meanwhile, abort this request.
				return
			}

			updateLastContact()

			// The 10 seconds threshold is arbitrary. The only criterion is
			// that it must be higher than raft’s HeartbeatTimeout of 2s. The
			// higher it is chosen, the longer users have to wait until they
			// can connect to a different node. Note that in the worst case,
			// |pingInterval| = 20s needs to pass before this threshold is
			// evaluated.
			if node.State() != raft.Leader && time.Since(lastContact) > 10*time.Second {
				// This node is neither the leader nor was it recently in
				// contact with the master, indicating that it is partitioned
				// from the rest of the network. We abort this GetMessages
				// request so that clients can connect to a different server
				// and receive new messages.
				log.Printf("Aborting GetMessages request due to LastContact (%v) too long ago\n", node.LastContact())
				return
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

	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, nopCloser{bytes.NewBuffer(nil)})
		return
	}

	b := make([]byte, 128)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, fmt.Sprintf("Cannot generate SessionAuth cookie: %v", err), http.StatusInternalServerError)
		return
	}
	sessionauth := fmt.Sprintf("%x", b)

	msg := &types.RobustMessage{
		Type: types.RobustCreateSession,
		Data: sessionauth,
	}
	if err := applyMessageWait(msg, 10*time.Second); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{bytes.NewBuffer(nil)})
			return
		}
		if err == ircserver.ErrSessionLimitReached {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
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

	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	msg := &types.RobustMessage{
		Session: session,
		Type:    types.RobustDeleteSession,
		Data:    req.Quitmessage,
	}
	if err := applyMessageWait(msg, 10*time.Second); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}

func handleLeader(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(node.Leader()))
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

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	ircServer.ConfigMu.RLock()
	defer ircServer.ConfigMu.RUnlock()
	w.Header().Set("X-RobustIRC-Config-Revision", strconv.FormatUint(ircServer.Config.Revision, 10))
	if err := toml.NewEncoder(w).Encode(&ircServer.Config); err != nil {
		log.Printf("Could not send TOML config: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func configRevision() uint64 {
	ircServer.ConfigMu.RLock()
	defer ircServer.ConfigMu.RUnlock()
	return ircServer.Config.Revision
}

func applyConfig(revision uint64, body string) error {
	if got, want := revision, configRevision(); got != want {
		return fmt.Errorf("Revision mismatch (got %d, want %d). Try again.", got, want)
	}

	msg := &types.RobustMessage{
		Type:     types.RobustConfig,
		Data:     body,
		Revision: revision + 1,
	}
	return applyMessageWait(msg, 10*time.Second)
}

func handlePostConfig(w http.ResponseWriter, r *http.Request) {
	revision, err := strconv.ParseUint(r.Header.Get("X-RobustIRC-Config-Revision"), 0, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var unused config.Network
	var body bytes.Buffer
	if _, err := toml.DecodeReader(io.TeeReader(r.Body, &body), &unused); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if node.State() != raft.Leader {
		maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	if err := applyConfig(revision, body.String()); err != nil {
		if err == raft.ErrNotLeader {
			maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}

		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}
