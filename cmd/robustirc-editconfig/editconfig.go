package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/robustirc/robustirc/robusthttp"
)

var (
	network = flag.String("network",
		"",
		`DNS name to connect to (e.g. "robustirc.net"). The _robustirc._tcp SRV record must be present.`)

	networkPassword = flag.String("network_password",
		"",
		"A secure password to protect the communication between raft nodes. Use pwgen(1) or similar.")
)

// TODO(secure): refactor this into util/
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

// getConfig obtains the RobustIRC network configuration from |server| and
// returns the TOML configuration as a string and its revision identifier
// (string) or an error.
func getConfig(server string) ([]byte, string, error) {
	url := fmt.Sprintf("https://%s/config", server)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return []byte{}, "", err
	}
	resp, err := robusthttp.Client(*networkPassword, true).Do(req)
	if err != nil {
		return []byte{}, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		ioutil.ReadAll(resp.Body)
		return []byte{}, "", fmt.Errorf("%s: Expected OK, got %v", url, resp.Status)
	}

	config, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, "", err
	}
	return config, resp.Header.Get("X-Robustirc-Config-Revision"), err
}

func postConfig(server string, revision string, config io.Reader) error {
	url := fmt.Sprintf("https://%s/config", server)
	req, err := http.NewRequest("POST", url, config)
	if err != nil {
		return err
	}
	req.Header.Set("X-RobustIRC-Config-Revision", revision)
	resp, err := robusthttp.Client(*networkPassword, true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("%s: Expected OK, got %v (%q)",
			url, resp.Status, string(body))
	}
	ioutil.ReadAll(resp.Body)
	return nil
}

func main() {
	flag.Parse()

	servers := resolveNetwork()
	config, revision, err := getConfig(servers[0])
	if err != nil {
		log.Fatal(err)
	}

	tmp, err := ioutil.TempFile("", "robustirc-editconfig-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(config); err != nil {
		log.Fatal(err)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, tmp.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("process returned with non-zero exit code, discarding changes\n")
		return
	}

	if _, err := tmp.Seek(0, 0); err != nil {
		log.Fatal(err)
	}

	if err := postConfig(servers[0], revision, tmp); err != nil {
		log.Printf("Find your edited configuration in %q\n", tmp.Name())
		// log.Fatal does not run deferred functions, so the tempfile will not
		// be deleted.
		log.Fatal(err)
	}

	log.Printf("Configuration updated.\n")
}
