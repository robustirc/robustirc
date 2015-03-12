package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/util"
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
		5*time.Minute,
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

func allNodesUpdated(statuses map[string]util.ServerStatus, binaryHash string) bool {
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
	if statuses, err := util.EnsureNetworkHealthy(servers, *networkPassword); err != nil {
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
			var statuses map[string]util.ServerStatus
			var err error

			started := time.Now()
			for time.Since(started) < *networkHealthTimeout {
				statuses, err = util.EnsureNetworkHealthy(servers, *networkPassword)
				if err != nil {
					log.Printf("Network is not healthy: %v\n", err)
					time.Sleep(1 * time.Second)
					continue
				}
				log.Printf("Network became healthy.\n")
				break
			}
			if err != nil {
				log.Fatalf("Network did not become healthy within %v, aborting. (reason: %v)\n", *networkHealthTimeout, err)
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
				log.Printf("%v\n", err)
			}

			for htry := 0; htry < 60; htry++ {
				time.Sleep(1 * time.Second)
				current, err := util.GetServerStatus(server, *networkPassword)
				if err != nil {
					log.Printf("Node unhealthy: %v\n", err)
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
