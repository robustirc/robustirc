package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/types"
)

func (api *HTTP) HandleKill(w http.ResponseWriter, r *http.Request) {
	var body bytes.Buffer
	r.Body = nopCloser{io.TeeReader(r.Body, &body)}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	for _, sessionid := range r.Form["session"] {
		id, err := strconv.ParseInt(sessionid, 0, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		msg := &types.RobustMessage{
			Session: types.RobustId{Id: id},
			Type:    types.RobustDeleteSession,
			Data:    "killed",
		}
		if err := api.applyMessageWait(msg, 10*time.Second); err != nil {
			if err == raft.ErrNotLeader {
				api.maybeProxyToLeader(w, r, nopCloser{&body})
				return
			}
			http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "killed %d\n", id)
	}
}
