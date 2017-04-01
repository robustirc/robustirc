package health

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/robustirc/robustirc/internal/robusthttp"
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
	CurrentTime    time.Time
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
	resp, err := robusthttp.Client(networkPassword, true).Do(req)
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

func CollectStatuses(servers []string, networkPassword string) (map[string]ServerStatus, error) {
	var (
		statuses   = make(map[string]ServerStatus, len(servers))
		statusesMu sync.Mutex
		g          errgroup.Group
	)
	setStatus := func(status ServerStatus) {
		statusesMu.Lock()
		defer statusesMu.Unlock()
		statuses[status.Server] = status
	}
	for _, server := range servers {
		server := server // capture range variable
		g.Go(func() error {
			status, err := GetServerStatus(server, networkPassword)
			if err != nil {
				return err
			}
			status.Server = server
			setStatus(status)
			return nil
		})
	}
	return statuses, g.Wait()
}
