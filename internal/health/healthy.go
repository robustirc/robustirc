package health

import (
	"fmt"
	"time"
)

// EnsureNetworkHealthy returns nil when all of the following is true:
//  • all nodes are reachable
//  • all nodes return the same leader
//  • all nodes are either follower or leader (i.e. not candidate/initializing)
//  • all follower nodes were recently contacted by the leader
func EnsureNetworkHealthy(servers []string, networkPassword string) (map[string]ServerStatus, error) {
	var leader string

	statuses, err := CollectStatuses(servers, networkPassword)
	if err != nil {
		return statuses, err
	}

	for _, status := range statuses {
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
