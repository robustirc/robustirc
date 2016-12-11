package api

import "net/http"

func (api *HTTP) HandleLeader(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(api.raftNode.Leader()))
}
