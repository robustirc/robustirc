package mod_test

import (
	"io/ioutil"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/robustirc/bridge/robustsession"
	"github.com/robustirc/robustirc/internal/health"
	"github.com/robustirc/robustirc/internal/localnet"
	"github.com/robustirc/robustirc/types"
)

func TestMessageOfDeath(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "robustirc-message-of-death-")
	if err != nil {
		t.Fatalf("Could not create tempdir: %v", err)
	}

	l, err := localnet.NewLocalnet(-1, tempdir)
	if err != nil {
		t.Fatalf("Could not start local RobustIRC network: %v", err)
	}
	defer l.Kill(true)

	l.EnablePanicCommand = "1"

	// For each of the nodes, start a goroutine that verifies that the node crashes, then start it again
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		cmd, tempdir, addr := l.StartIRCServer(i == 0)
		wg.Add(1)
		go func(cmd *exec.Cmd, tempdir string, addr string) {
			defer wg.Done()

			terminated := make(chan error)
			skipped := make(chan bool)

			go func() {
				terminated <- cmd.Wait()
			}()

			go func() {
				// Poll messages of death counter.
				parser := &expfmt.TextParser{}
				for {
					time.Sleep(50 * time.Millisecond)
					req, err := http.NewRequest("GET", addr+"metrics", nil)
					if err != nil {
						continue
					}
					resp, err := l.Httpclient.Do(req)
					if err != nil {
						continue
					}
					if resp.StatusCode != http.StatusOK {
						continue
					}
					metrics, err := parser.TextToMetricFamilies(resp.Body)
					if err != nil {
						continue
					}
					applied, ok := metrics["applied_messages"]
					if !ok {
						continue
					}
					for _, m := range applied.GetMetric() {
						for _, labelpair := range m.GetLabel() {
							if labelpair.GetName() == "type" &&
								labelpair.GetValue() == types.RobustType(types.RobustMessageOfDeath).String() {
								if m.GetCounter().GetValue() > 0 {
									skipped <- true
								}
							}
						}
					}
				}
			}()

			// Wait for the server to either crash or skip a message of death.
			select {
			case <-terminated:
				t.Logf("Node terminated (as expected)")
			case <-skipped:
				t.Logf("Node skipped message of death")
			}

			// Run restart.sh for that node.
			rcmd := exec.Command(filepath.Join(tempdir, "restart.sh"))
			if err := rcmd.Start(); err != nil {
				t.Errorf("Cannot restart node: %v", err)
				return
			}
			l.RecordResource("pid", strconv.Itoa(cmd.Process.Pid))

			// Ensure the node comes back up.
			started := time.Now()
			for time.Since(started) < 10*time.Second {
				if _, err := health.GetServerStatus(addr, l.NetworkPassword); err != nil {
					t.Logf("Node %s unhealthy: %v", addr, err)
					time.Sleep(1 * time.Second)
					continue
				}
				t.Logf("Node %s became healthy", addr)
				return
			}
			t.Errorf("Node did not become healthy within 10s")
		}(cmd, tempdir, addr)
	}

	// Connect and send the PANIC message.
	session, err := robustsession.Create(strings.Join(l.Servers(), ","), filepath.Join(tempdir, "cert.pem"))
	if err != nil {
		t.Fatalf("Could not create robustsession: %v", err)
	}
	foundjoin := make(chan bool)
	go func() {
		for msg := range session.Messages {
			if !strings.HasPrefix(msg, ":mod!1@") ||
				!strings.HasSuffix(msg, " JOIN #mod") {
				continue
			}
			select {
			case foundjoin <- true:
			default:
				t.Errorf("Found JOIN too early or too late (channel write did not block)")
			}
		}
	}()
	go func() {
		for err := range session.Errors {
			t.Errorf("RobustSession error: %v", err)
		}
	}()

	session.PostMessage("NICK mod")
	session.PostMessage("USER 1 2 3 4")
	session.PostMessage("PANIC")

	t.Logf("Message of death sent")

	wg.Wait()

	healthy := false
	for try := 0; try < 5; try++ {
		if l.Healthy() {
			healthy = true
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !healthy {
		t.Fatalf("Expected recovery, but not all nodes are healthy")
	}

	// Verify sending a JOIN now results in an output message.
	session.PostMessage("JOIN #mod")
	select {
	case <-foundjoin:
		t.Logf("JOIN reply received, network progressing")
	case <-time.After(10 * time.Second):
		t.Errorf("Timeout waiting for JOIN message")
	}
}
