package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/hashicorp/raft"
)

func (api *HTTP) handleJoin(w http.ResponseWriter, r *http.Request) {
	log.Println("Join request from", r.RemoteAddr)
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

	log.Printf("Adding peer %q to the network.\n", req.Addr)

	if api.raftProtocolVersion < 3 {
		if err := api.raftNode.AddPeer(raft.ServerAddress(req.Addr)).Error(); err != nil {
			log.Println("Could not add peer:", err)
			http.Error(w, "Could not add peer", http.StatusInternalServerError)
			return
		}
	} else {
		idxf := api.raftNode.AddVoter(
			raft.ServerID(req.Addr),
			raft.ServerAddress(req.Addr),
			0, // prevIndex of 0 means other config changes can happen
			0) // no timeout
		if err := idxf.Error(); err != nil {
			log.Println("Could not add peer:", err)
			http.Error(w, "Could not add peer", http.StatusInternalServerError)
			return
		}
	}
}
