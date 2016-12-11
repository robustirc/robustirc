package api

import "net/http"

func (api *HTTP) handleLeader(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(api.raftNode.Leader()))
}
