package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/raft"
)

func (api *HTTP) HandlePart(w http.ResponseWriter, r *http.Request) {
	log.Println("Part request from", r.RemoteAddr)
	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, r.Body)
		return
	}

	type partRequest struct {
		Addr string
	}
	var req partRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(w, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Removing peer %q from the network.\n", req.Addr)

	if err := api.raftNode.RemovePeer(req.Addr).Error(); err != nil && err != raft.ErrKnownPeer {
		log.Println("Could not remove peer:", err)
		http.Error(w, "Could not remove peer", http.StatusInternalServerError)
		return
	}
}
