package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"
)

func (api *HTTP) configRevision() uint64 {
	i := api.ircServer
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.Revision
}

func (api *HTTP) applyConfig(revision uint64, body string) error {
	if got, want := revision, api.configRevision(); got != want {
		return fmt.Errorf("Revision mismatch (got %d, want %d). Try again.", got, want)
	}

	msg := &types.RobustMessage{
		Type:     types.RobustConfig,
		Data:     body,
		Revision: revision + 1,
	}
	return api.applyMessageWait(msg, 10*time.Second)
}

func (api *HTTP) handlePostConfig(w http.ResponseWriter, r *http.Request) {
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

	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	if err := api.applyConfig(revision, body.String()); err != nil {
		if err == raft.ErrNotLeader {
			api.maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}

		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}
