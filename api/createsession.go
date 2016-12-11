package api

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/types"
)

func (api *HTTP) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, nopCloser{bytes.NewBuffer(nil)})
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
	if err := api.applyMessageWait(msg, 10*time.Second); err != nil {
		if err == raft.ErrNotLeader {
			api.maybeProxyToLeader(w, r, nopCloser{bytes.NewBuffer(nil)})
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

	if err := json.NewEncoder(w).Encode(createSessionReply{sessionid, sessionauth, api.network}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}
