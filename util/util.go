package util

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/robustirc/internal/robusthttp"
)

func SetNetworkConfig(servers []string, config, networkPassword string) error {
	server := servers[0]
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s/config", server), nil)
	if err != nil {
		return err
	}
	resp, err := robusthttp.Client(networkPassword, true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Expected HTTP OK, got %v", resp.Status)
	}

	req, err = http.NewRequest("POST", fmt.Sprintf("https://%s/config", server), bytes.NewBuffer([]byte(config)))
	if err != nil {
		return err
	}
	req.Header.Set("X-RobustIRC-Config-Revision", resp.Header.Get("X-RobustIRC-Config-Revision"))
	resp, err = robusthttp.Client(networkPassword, true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Expected HTTP OK, got %v", resp.Status)
	}

	return nil
}
