package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/types"
)

func (api *HTTP) handleDeleteSession(w http.ResponseWriter, r *http.Request, session types.RobustId) {
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

	msg := &types.RobustMessage{
		Session: session,
		Type:    types.RobustDeleteSession,
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
