package util

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/robustirc/robustirc/robusthttp"
	"github.com/stapelberg/glog"
)

type ServerStatus struct {
	Server string

	State          string
	Leader         string
	Peers          []string
	AppliedIndex   uint64
	CommitIndex    uint64
	LastContact    time.Time
	ExecutableHash string
}

func GetServerStatus(server, networkPassword string) (ServerStatus, error) {
	var status ServerStatus
	if !strings.HasPrefix(server, "https://") {
		server = fmt.Sprintf("https://%s/", server)
	}
	req, err := http.NewRequest("GET", server, nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := robusthttp.Client(networkPassword).Do(req)
	if err != nil {
		return status, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("Expected HTTP OK, got %v", resp.Status)
	}

	err = json.NewDecoder(resp.Body).Decode(&status)
	ioutil.ReadAll(resp.Body)
	return status, err
}

// EnsureNetworkHealthy returns nil when all of the following is true:
//  • all nodes are reachable
//  • all nodes return the same leader
//  • all nodes are either follower or leader (i.e. not candidate/initializing)
//  • all follower nodes were recently contacted by the leader
func EnsureNetworkHealthy(servers []string, networkPassword string) (map[string]ServerStatus, error) {
	var leader string
	statusChan := make(chan ServerStatus, len(servers))
	errChan := make(chan error, len(servers))
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			status, err := GetServerStatus(server, networkPassword)
			if err != nil {
				errChan <- err
				return
			}
			status.Server = server
			statusChan <- status
		}(server)
	}
	wg.Wait()
	close(errChan)
	close(statusChan)

	statuses := make(map[string]ServerStatus, len(servers))

	for err := range errChan {
		return statuses, err
	}

	for status := range statusChan {
		statuses[status.Server] = status

		// No error checking since this was _parsed_ from JSON, so it must be valid.
		pretty, _ := json.MarshalIndent(status, "", "  ")
		glog.Infof("%s\n", pretty)

		if status.State != "Leader" && status.State != "Follower" {
			return statuses, fmt.Errorf("Server %q in state %q, need Leader or Follower",
				status.Server, status.State)
		}
		if leader == "" {
			leader = status.Leader
		} else if leader != status.Leader {
			return statuses, fmt.Errorf("Server %q thinks %q is leader, others think %q is leader",
				status.Server, status.Leader, leader)
		}
		if status.State == "Follower" && time.Since(status.LastContact) > 2*time.Second {
			return statuses, fmt.Errorf("Server %q was last contacted by the leader at %v, which is over 2 seconds ago",
				status.Server, status.LastContact)
		}
	}
	if leader == "" {
		return statuses, fmt.Errorf("There is no leader currently")
	}
	return statuses, nil
}
