// robustirc-localnet starts 3 RobustIRC servers on localhost on random ports
// with temporary data directories, generating a self-signed SSL certificate.
// stdout and stderr are redirected to a file in the temporary data directory
// of each node.
//
// robustirc-localnet can be used for playing around with RobustIRC, especially
// when developing.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/robustirc/robustirc/internal/localnet"
)

var (
	localnetDir = flag.String("localnet_dir",
		"~/.config/robustirc-localnet",
		"Directory in which to keep state for robustirc-localnet (SSL certificates, PID files, etc.)")

	stop = flag.Bool("stop",
		false,
		"Whether to stop the currently running localnet instead of starting a new one")

	deleteTempdirs = flag.Bool("deleteTempdirs",
		true,
		"If false, temporary directories are left behind for manual inspection")

	port = flag.Int("port",
		-1,
		"Port to (try to) use for the first RobustIRC server. If in use, another will be tried.")

	startShell = flag.Bool("shell",
		false,
		"Enter interactive shell once RobustIRC network is brought up")
)

const config = `
SessionExpiration = "10m0s"

# Disable throttling for benchmarks
PostMessageCooloff = "0"

[IRC]
  [[IRC.Operators]]
    Name = "foo"
    Password = "bar"

  [[IRC.Services]]
    Password = "mypass"

[TrustedBridges]
  "1234567890abcdef1234567890abcdef" = "localnet-bridge"
`

type process struct {
	name      string
	shorthand string
	process   *os.Process
	tempdir   string
}

var errUsage = errors.New("sentinel error: print usage")

func kill(process **os.Process) {
	if *process == nil {
		fmt.Println("already killed. use restart command first")
		return
	}
	fmt.Printf("killing pid %v\n", (*process).Pid)
	(*process).Kill()
	*process = nil
}

func restart(process **os.Process, dir string) {
	if *process != nil {
		kill(process)
	}
	cmd := exec.Command(filepath.Join(dir, "restart.sh"))
	if err := cmd.Start(); err != nil {
		fmt.Println(err)
		return
	}
	*process = cmd.Process
	go func() {
		cmd.Wait() // reap child process
	}()
}

func (s *shell) cmdKill(args []string) error {
	if len(args) == 0 {
		return errUsage
	}

	for _, p := range s.processes {
		if args[0] != p.name && args[0] != p.shorthand {
			continue
		}
		kill(&p.process)
		return nil
	}
	return fmt.Errorf("invalid argument %q: expected one of bridge, node1, node2, node", args[0])
}

func (s *shell) cmdRestart(args []string) error {
	if len(args) == 0 {
		return errUsage
	}

	for _, p := range s.processes {
		if args[0] != p.name && args[0] != p.shorthand {
			continue
		}
		restart(&p.process, p.tempdir)
		return nil
	}
	return fmt.Errorf("invalid argument %q: expected one of bridge, node1, node2, node", args[0])
}

type shell struct {
	processes []*process
}

func (s *shell) run() error {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("robustirc-localnet> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		usage := func() {
			fmt.Print(`
Help:
	kill <nodeN>|<bridge>
	restart <nodeN>

`)
		}
		if len(fields) == 0 || strings.EqualFold(fields[0], "help") {
			usage()
			continue
		}
		var err error
		switch fields[0] {
		default:
			err = errUsage

		case "kill", "k":
			err = s.cmdKill(fields[1:])

		case "restart", "r":
			err = s.cmdRestart(fields[1:])
		}
		if err != nil {
			if err == errUsage {
				usage()
			} else {
				log.Print(err)
			}
			continue
		}
	}
	return scanner.Err()
}

func main() {
	flag.Parse()

	l, err := localnet.NewLocalnet(*port, *localnetDir)
	if err != nil {
		log.Fatalf("%v", err)
	}

	if *stop {
		l.Kill(*deleteTempdirs)
		return
	}

	if l.Running() {
		log.Fatalf("There already is a localnet instance running. Either use -stop or specify a different -localnet_dir")
	}

	success := false

	defer func() {
		if success {
			return
		}
		log.Printf("Could not successfully set up localnet, cleaning up.\n")
		l.Kill(*deleteTempdirs)
	}()

	node1, node1dir, _ := l.StartIRCServer(true, flag.Args()...)
	node2, node2dir, _ := l.StartIRCServer(false, flag.Args()...)
	node3, node3dir, _ := l.StartIRCServer(false, flag.Args()...)
	bridge, bridgedir := l.StartBridge()
	processes := []*process{
		{"node1", "1", node1.Process, node1dir},
		{"node2", "2", node2.Process, node2dir},
		{"node3", "3", node3.Process, node3dir},
		{"bridge", "b", bridge.Process, bridgedir},
	}

	try := 0
	for try < 10 {
		try++
		if l.Healthy() {
			success = true
			break
		} else {
			time.Sleep(1 * time.Second)
		}
	}

	if err := l.SetConfig(config); err != nil {
		log.Printf("Could not change config: %v\n", err)
	}

	if *startShell {
		s := shell{
			processes: processes,
		}
		if err := s.run(); err != nil {
			log.Print(err)
		}
	}
}
