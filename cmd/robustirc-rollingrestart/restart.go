package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robustirc/robustirc/robusthttp"
	"github.com/stapelberg/glog"
)

var (
	binaryPath = flag.String("binary_path",
		"robustirc",
		"Path to the robustirc binary.")

	network = flag.String("network",
		"",
		`DNS name to connect to (e.g. "robustirc.net"). The _robustirc._tcp SRV record must be present.`)

	networkPassword = flag.String("network_password",
		"",
		"A secure password to protect the communication between raft nodes. Use pwgen(1) or similar.")

	networkHealthTimeout = flag.Duration("network_health_timeout",
		1*time.Minute,
		"Amount of time until rollingrestart gives up waiting for the network to become healthy again")
)

func fileHash(path string) string {
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		log.Fatal(err)
	}

	return fmt.Sprintf("%.16x", h.Sum(nil))
}

func resolveNetwork() []string {
	var servers []string

	parts := strings.Split(*network, ",")
	if len(parts) > 1 {
		log.Printf("Interpreting %q as list of servers instead of network name\n", *network)
		return parts
	}

	_, addrs, err := net.LookupSRV("robustirc", "tcp", *network)
	if err != nil {
		log.Fatal(err)
	}
	for _, addr := range addrs {
		target := addr.Target
		if target[len(target)-1] == '.' {
			target = target[:len(target)-1]
		}
		servers = append(servers, fmt.Sprintf("%s:%d", target, addr.Port))
	}

	return servers
}

type serverStatus struct {
	Server string

	State          string
	Leader         string
	Peers          []string
	AppliedIndex   uint64
	CommitIndex    uint64
	LastContact    time.Time
	ExecutableHash string
}

func getServerStatus(server string) (serverStatus, error) {
	var status serverStatus
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s/", server), nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := robusthttp.Client(*networkPassword).Do(req)
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

func quit(server string) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("https://%s/quit", server), nil)
	if err != nil {
		return err
	}
	resp, err := robusthttp.Client(*networkPassword).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The server cannot respond with a proper reply, because it is terminating.
	return nil
}

// ensureNetworkHealthy returns nil when all of the following is true:
//  • all nodes are reachable
//  • all nodes return the same leader
//  • all nodes are either follower or leader (i.e. not candidate/initializing)
//  • all follower nodes were recently contacted by the leader
func ensureNetworkHealthy(servers []string) (map[string]serverStatus, error) {
	var leader string
	statusChan := make(chan serverStatus, len(servers))
	errChan := make(chan error, len(servers))
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			status, err := getServerStatus(server)
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

	statuses := make(map[string]serverStatus, len(servers))

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
		if status.State == "Follower" && time.Since(status.LastContact) > 1*time.Second {
			return statuses, fmt.Errorf("Server %q was last contacted by the leader at %v, which is over a second ago",
				status.Server, status.LastContact)
		}
	}
	if leader == "" {
		return statuses, fmt.Errorf("There is no leader currently")
	}
	return statuses, nil
}

func allNodesUpdated(statuses map[string]serverStatus, binaryHash string) bool {
	for _, status := range statuses {
		if status.ExecutableHash != binaryHash {
			return false
		}
	}
	return true
}

func main() {
	defer glog.Flush()
	glog.CopyStandardLogTo("INFO")
	flag.Parse()
	if strings.TrimSpace(*network) == "" {
		log.Fatalf("You need to specify -network")
	}

	binaryHash := fileHash(*binaryPath)
	glog.Infof("binaryHash = %s", binaryHash)

	servers := resolveNetwork()
	log.Printf("Checking network health\n")
	if statuses, err := ensureNetworkHealthy(servers); err != nil {
		log.Fatalf("Aborting upgrade for safety: %v", err)
	} else {
		if allNodesUpdated(statuses, binaryHash) {
			log.Printf("All nodes are already running the requested version.\n")
			return
		}
	}

	log.Printf("Restarting %q nodes until their binary hash is %s\n", *network, binaryHash)

	for rtry := 0; rtry < 5; rtry++ {
		for _, server := range servers {
			var statuses map[string]serverStatus
			var err error

			started := time.Now()
			for time.Since(started) < *networkHealthTimeout {
				statuses, err = ensureNetworkHealthy(servers)
				if err != nil {
					log.Printf("Network is not healthy: %v\n", err)
					time.Sleep(1 * time.Second)
					continue
				}
				break
			}

			if statuses[server].ExecutableHash == binaryHash {
				if allNodesUpdated(statuses, binaryHash) {
					log.Printf("All done!\n")
					return
				}
				log.Printf("Skipping %q which is already running the requested version\n", server)
				continue
			}

			lastApplied := statuses[server].AppliedIndex

			log.Printf("Killing node %q\n", server)
			if err := quit(server); err != nil {
				log.Printf("Quitting %q: %v\n", server, err)
			}

			for htry := 0; htry < 60; htry++ {
				time.Sleep(1 * time.Second)
				current, err := getServerStatus(server)
				if err != nil {
					log.Printf("Node %q unhealthy: %v\n", server, err)
					continue
				}
				if current.ExecutableHash != binaryHash {
					log.Printf("Node %q came up with hash %s instead of %s?!\n",
						server, current, binaryHash)
					break
				}
				if current.AppliedIndex < lastApplied {
					log.Printf("Node %q has not yet applied all messages it saw before, waiting (got %d, want ≥ %d)\n",
						server, current.AppliedIndex, lastApplied)
					continue
				}
				log.Printf("Node %q was upgraded and is healthy again\n", server)
				break
			}
		}
	}

	log.Printf("All done!\n")
}
