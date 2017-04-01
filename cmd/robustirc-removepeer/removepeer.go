package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/health"
	"github.com/robustirc/robustirc/internal/robusthttp"
	"github.com/stapelberg/glog"
)

var (
	network = flag.String("network",
		"",
		`DNS name to connect to (e.g. "robustirc.net"). The _robustirc._tcp SRV record must be present.`)

	networkPassword = flag.String("network_password",
		"",
		"A secure password to protect the communication between raft nodes. Use pwgen(1) or similar.")

	removePeer = flag.String("remove_peer",
		"",
		"Address of the peer (-peer_addr) to be removed.")
)

func remove(server, peer string) error {
	type partRequest struct {
		Addr string
	}
	var buf *bytes.Buffer
	if data, err := json.Marshal(partRequest{peer}); err != nil {
		log.Fatal("Could not marshal part request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("https://%s/part", server), buf)
	if err != nil {
		return err
	}
	resp, err := robusthttp.Client(*networkPassword, true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Expected status OK, got %v", resp.Status)
	}

	return nil
}

func peerListsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// quorumSize is like Raft.quorumSize(), except that peers are _all_ peers in
// the network, whereas raft.quorumSize() excludes the local node.
func quorumSize(peers []string) int {
	return (len(peers) / 2) + 1
}

func main() {
	defer glog.Flush()
	glog.CopyStandardLogTo("INFO")
	flag.Parse()
	if strings.TrimSpace(*network) == "" {
		glog.Errorf("You need to specify -network")
		os.Exit(1)
	}
	if strings.TrimSpace(*removePeer) == "" {
		glog.Errorf("You need to specify -removePeer")
		os.Exit(1)
	}

	servers := health.ResolveNetwork(*network)
	var peers []string
	statuses, err := health.CollectStatuses(servers, *networkPassword)
	if len(statuses) == 0 {
		glog.Errorf("No node within -network=%q reachable: %v", *network, err)
		os.Exit(1)
	}
	for server, status := range statuses {
		glog.Infof("server %q status %v, peers %v\n", server, status, status.Peers)
		if peers == nil {
			peers = status.Peers
		} else {
			if !peerListsEqual(peers, status.Peers) {
				glog.Errorf("Server %q has peers %v which is different from %v, network inconsistent, aborting", server, status.Peers, peers)
				os.Exit(1)
			}
		}
	}

	removePeerFound := false
	var leader string
	statuses, err = health.CollectStatuses(peers, *networkPassword)
	healthy := make(map[string]bool)
	for _, server := range peers {
		if server == *removePeer {
			removePeerFound = true
		}
		status, ok := statuses[server]
		if !ok {
			continue
		}
		if status.State == raft.Leader.String() {
			leader = server
			healthy[server] = true
		}
		if status.State == raft.Follower.String() {
			healthy[server] = true
		}
	}
	glog.Infof("healthy = %v\n", healthy)

	if leader == "" {
		glog.Errorf("No leader found, network inconsistent, aborting")
		os.Exit(1)
	}

	if !removePeerFound {
		glog.Errorf("-removePeer=%q not found in list of peers (%v) in network %q\n", *removePeer, peers, *network)
		os.Exit(1)
	}

	if len(healthy) < 3 {
		glog.Errorf("The only 2 healthy nodes are %v, cannot remove any more nodes or the network will freeze.\n", healthy)
		os.Exit(1)
	}

	if healthy[*removePeer] && len(healthy)-1 < quorumSize(peers) {
		glog.Errorf("Cannot remove healthy peer %q, this would push the network below quorum size (%d of %d peers needed for quorum).\n", *removePeer, quorumSize(peers), len(peers))
		os.Exit(1)
	}

	if err := remove(servers[0], *removePeer); err != nil {
		glog.Errorf(err.Error())
		os.Exit(1)
	}
	log.Printf("Node %q removed. It is now safe to kill the robustirc process on that node and remove the data.\n", *removePeer)
}
