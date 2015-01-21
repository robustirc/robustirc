package main

import (
	"crypto/sha256"
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

func serverVersion(server string) (string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s/executablehash", server), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("robustirc", *networkPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Expected HTTP OK, got %v", resp.Status)
	}

	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}

func quit(server string) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("https://%s/quit", server), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("robustirc", *networkPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The server cannot respond with a proper reply, because it is terminating.
	return nil
}

func main() {
	flag.Parse()
	if strings.TrimSpace(*network) == "" {
		log.Fatalf("You need to specify -network")
	}

	binaryHash := fileHash(*binaryPath)

	servers := resolveNetwork()
	log.Printf("Checking network health\n")

	// Check that all nodes are healthy (in parallel).
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(server string) {
			current, err := serverVersion(server)
			if err != nil {
				log.Fatalf("Node %q unhealthy, aborting upgrade for safety. (%v)", server, err)
			}

			log.Printf(" - binary hash %s on %s\n", current, server)

			wg.Done()
		}(server)
	}
	wg.Wait()

	log.Printf("Restarting %q nodes until their binary hash is %s\n", *network, binaryHash)

	for rtry := 0; rtry < 5; rtry++ {
		for _, server := range servers {
			// Skip this server if it’s already running the target version.
			if current, _ := serverVersion(server); current == binaryHash {
				continue
			}

			log.Printf("Killing node %q\n", server)
			if err := quit(server); err != nil {
				log.Printf("Quitting %q: %v\n", server, err)
			}

			for htry := 0; htry < 60; htry++ {
				time.Sleep(1 * time.Second)
				current, err := serverVersion(server)
				if err != nil {
					log.Printf("Node %q unhealthy: %v\n", server, err)
					continue
				}
				if current == binaryHash {
					break
				}
				log.Printf("Node %q came up with hash %s instead of %s?!\n", server, current, binaryHash)
				break
			}
		}
	}
}
