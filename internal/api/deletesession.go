package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/robust"
)

func (api *HTTP) handleDeleteSession(w http.ResponseWriter, r *http.Request, session robust.Id) {
	var req struct {
		Quitmessage string
	}

	var body bytes.Buffer
	rd := io.TeeReader(r.Body, &body)
	if err := json.NewDecoder(rd).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Could not decode request: %v", err), http.StatusInternalServerError)
		return
	}

	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	msg := &robust.Message{
		Session: session,
		Type:    robust.DeleteSession,
		Data:    req.Quitmessage,
	}
	if err := api.applyMessageWait(msg, 10*time.Second); err != nil {
		if err == raft.ErrNotLeader {
			api.maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}
