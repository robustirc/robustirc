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
	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/robust"
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

	msg := &robust.Message{
		Type: robust.CreateSession,
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

	if err := json.NewEncoder(w).Encode(struct {
		Sessionid   string
		Sessionauth string
		Prefix      string
	}{
		Sessionid:   sessionid,
		Sessionauth: sessionauth,
		Prefix:      api.network,
	}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}
