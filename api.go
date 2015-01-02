package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"fancyirc/types"

	"github.com/gorilla/mux"
	"github.com/hashicorp/raft"
)

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
		return types.FancyId(0), fmt.Errorf("Invalid session: %v", err)
	}

	return types.FancyId(id), nil
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

	a, err := net.ResolveTCPAddr("tcp", req.Addr)
	if err != nil {
		log.Printf("Could not resolve addr %q: %v\n", req.Addr, err)
		http.Error(w, "Could not resolve your address", 400)
		return
	}

	if err := node.AddPeer(a).Error(); err != nil {
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// TODO: read params
	// TODO: register in stats that we are sending

	if r.FormValue("lastseen") != "" {
		lastSeen, err := strconv.ParseInt(r.FormValue("lastseen"), 0, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid value for lastseen: %v", err),
				http.StatusInternalServerError)
			return
		}

		// TODO(secure): don’t send everything, skip everything until the last seen message
		log.Printf("need to skip to %d\n", lastSeen)
	}

	enc := json.NewEncoder(w)
	s := sessions[session]
	for idx := 0; ; idx++ {
		msg := GetMessage(idx)

		if !s.interestedIn(msg) {
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
	msg := types.NewFancyMessage(types.FancyCreateSession, types.FancyId(0), "")
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

	sessionid := fmt.Sprintf("0x%x", msg.Id)

	w.Header().Set("Content-Type", "application/json")

	type createSessionReply struct {
		Sessionid string
		Prefix    string
	}

	if err := json.NewEncoder(w).Encode(createSessionReply{sessionid, *network}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionid := mux.Vars(r)["sessionid"]
	log.Printf("TODO: delete session %q\n", sessionid)
}
