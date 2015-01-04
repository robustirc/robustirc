package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fancyirc/ircserver"
	"fancyirc/types"

	"github.com/gorilla/mux"
	"github.com/hashicorp/raft"
)

var (
	GetMessageRequests = make(map[string]GetMessageStats)
)

type GetMessageStats struct {
	Session types.FancyId
	Nick    string
	Started time.Time
}

func (stats GetMessageStats) StartedAndRelative() string {
	return stats.Started.Format("2006-01-02 15:04:05 -07:00") + " (" +
		time.Now().Round(time.Second).Sub(stats.Started.Round(time.Second)).String() + " ago)"
}

func redirectToLeader(w http.ResponseWriter, r *http.Request) {
	leader := node.Leader()
	if leader == nil {
		http.Error(w, fmt.Sprintf("No leader known. Please try another server."),
			http.StatusInternalServerError)
		return
	}

	target := r.URL
	target.Scheme = "http"
	target.Host = leader.String()
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func sessionForRequest(r *http.Request) (types.FancyId, error) {
	idstr := mux.Vars(r)["sessionid"]
	id, err := strconv.ParseInt(idstr, 0, 64)
	if err != nil {
		return types.FancyId{}, fmt.Errorf("Invalid session: %v", err)
	}

	session := types.FancyId{Id: id}
	s, ok := ircserver.GetSession(session)
	if !ok {
		return types.FancyId{}, fmt.Errorf("No such session")
	}

	cookie, err := r.Cookie("SessionAuth")
	if err != nil {
		return types.FancyId{}, fmt.Errorf("No SessionAuth cookie set")
	}
	if cookie.Value != s.Auth {
		return types.FancyId{}, fmt.Errorf("Invalid SessionAuth cookie")
	}

	return session, nil
}

// handlePostMessage is called by the fancyproxy whenever a message should be
// posted. The handler blocks until either the data was written or an error
// occurred. If successful, it returns the unique id of the message.
func handlePostMessage(w http.ResponseWriter, r *http.Request) {
	session, err := sessionForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// TODO(secure): read at most 512 byte of body, as the IRC RFC restricts
	// messages to be that length. this also protects us from “let’s send a
	// large body” attacks.
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request:", err)
		return
	}

	// TODO(secure): properly check that we can convert data to a string at all.
	msg := types.NewFancyMessage(types.FancyIRCFromClient, session, string(data))
	msgbytes, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not store message, cannot encode it as JSON: %v", err),
			http.StatusInternalServerError)
		return
	}

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			redirectToLeader(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(msgbytes)
}

func handleJoin(w http.ResponseWriter, r *http.Request) {
	log.Println("Join request from", r.RemoteAddr)
	if node.State() != raft.Leader {
		redirectToLeader(w, r)
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

	if err := node.AddPeer(&dnsAddr{req.Addr}).Error(); err != nil {
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

func handleGetMessages(w http.ResponseWriter, r *http.Request) {
	session, err := sessionForRequest(r)
	if err != nil {
		log.Printf("invalid session: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s, _ := ircserver.GetSession(session)

	remoteAddr := r.RemoteAddr
	GetMessageRequests[remoteAddr] = GetMessageStats{
		Session: session,
		Nick:    s.Nick,
		Started: time.Now(),
	}
	defer func() {
		delete(GetMessageRequests, remoteAddr)
	}()

	lastSeen := s.StartId
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
		lastSeen = types.FancyId{
			Id:    first,
			Reply: last,
		}
		log.Printf("Trying to resume at %v\n", lastSeen)
	}

	enc := json.NewEncoder(w)
	for {
		msg := ircserver.GetMessage(lastSeen)
		s, ok := ircserver.GetSession(session)
		if !ok {
			// Session was deleted in the meanwhile.
			break
		}

		lastSeen = msg.Id

		interested := s.InterestedIn(msg)
		log.Printf("[DEBUG] Checking whether %+v is interested in %+v --> %v\n", s, msg, interested)
		if !interested {
			continue
		}

		if err := enc.Encode(msg); err != nil {
			log.Printf("Error encoding JSON: %v\n", err)
			return
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 128)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, fmt.Sprintf("Cannot generate SessionAuth cookie: %v", err), http.StatusInternalServerError)
		return
	}
	sessionauth := fmt.Sprintf("%x", b)
	msg := types.NewFancyMessage(types.FancyCreateSession, types.FancyId{}, sessionauth)
	// Cannot fail, no user input.
	msgbytes, _ := json.Marshal(msg)

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			redirectToLeader(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}

	sessionid := fmt.Sprintf("0x%x", msg.Id.Id)

	w.Header().Set("Content-Type", "application/json")
	http.SetCookie(w, &http.Cookie{
		Name:  "SessionAuth",
		Value: sessionauth,
		Path:  "/",
		// TODO(secure): make this configurable? we also need to make sure we use DNS names instead of ip:port pairs
		Domain:   "twice-irc.de",
		Expires:  time.Now().Add(50 * 365 * 24 * time.Hour),
		Secure:   true,
		HttpOnly: true,
	})

	type createSessionReply struct {
		Sessionid string
		Prefix    string
	}

	if err := json.NewEncoder(w).Encode(createSessionReply{sessionid, *network}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	session, err := sessionForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type deleteSessionRequest struct {
		Quitmessage string
	}

	var req deleteSessionRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Could not decode request: %v", err), http.StatusInternalServerError)
		return
	}

	msg := types.NewFancyMessage(types.FancyDeleteSession, session, req.Quitmessage)
	// Cannot fail, no user input.
	msgbytes, _ := json.Marshal(msg)

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			redirectToLeader(w, r)
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}
