package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/raft"
)

func (api *HTTP) handlePart(w http.ResponseWriter, r *http.Request) {
	log.Println("Part request from", r.RemoteAddr)
	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, r.Body)
		return
	}

	var req struct {
		Addr string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(w, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Removing peer %q from the network.\n", req.Addr)

	if api.raftProtocolVersion < 3 {
		if err := api.raftNode.RemovePeer(raft.ServerAddress(req.Addr)).Error(); err != nil {
			log.Println("Could not remove peer:", err)
			http.Error(w, "Could not remove peer", http.StatusInternalServerError)
			return
		}
	} else {
		idxf := api.raftNode.RemoveServer(
			raft.ServerID(req.Addr),
			0, // prevIndex of 0 means other config changes can happen
			0) // no timeout
		if err := idxf.Error(); err != nil {
			log.Println("Could not remove peer:", err)
			http.Error(w, "Could not remove peer", http.StatusInternalServerError)
			return
		}
	}
}
