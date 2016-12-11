package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/raft"
)

func (api *HTTP) HandleJoin(w http.ResponseWriter, r *http.Request) {
	log.Println("Join request from", r.RemoteAddr)
	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, r.Body)
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

	if err := api.raftNode.AddPeer(req.Addr).Error(); err != nil && err != raft.ErrKnownPeer {
		log.Println("Could not add peer:", err)
		http.Error(w, "Could not add peer", http.StatusInternalServerError)
		return
	}
}
