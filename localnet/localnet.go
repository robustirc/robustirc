package localnet

import (
	crypto_rand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/robustirc/robustirc/util"
)

func help(binary string) error {
	err := exec.Command(binary, "-help").Run()
	if exiterr, ok := err.(*exec.ExitError); ok {
		status, ok := exiterr.Sys().(syscall.WaitStatus)
		if !ok {
			log.Panicf("cannot run on this platform: exec.ExitError.Sys() does not return syscall.WaitStatus")
		}
		// -help results in exit status 2, so that’s expected.
		if status.ExitStatus() == 2 {
			return nil
		}
	}
	return err
}

func randomChar() (byte, error) {
	charset := "abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"0123456789"

	bn, err := crypto_rand.Int(crypto_rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, err
	}
	n := byte(bn.Int64())
	return charset[n], nil
}

func randomPassword(n int) (string, error) {
	pw := make([]byte, n)
	for i := 0; i < n; i++ {
		c, err := randomChar()
		if err != nil {
			return "", err
		}
		pw[i] = c
	}
	return string(pw), nil
}

type localnet struct {
	dir             string
	Ports           []int
	randomPort      int
	NetworkPassword string
	// An http.Client which has the generated SSL certificate in its list of root CAs.
	Httpclient *http.Client

	// EnablePanicCommand is passed to the RobustIRC server processes in the
	// ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND environment variable. If set to
	// "1", the PANIC command is enabled, otherwise disabled.
	EnablePanicCommand string
}

// RecordResource appends a line to a file in -localnet_dir so that we can
// clean up resources (tempdirs, pids) when being called with -stop later.
func (l *localnet) RecordResource(rtype string, value string) error {
	f, err := os.OpenFile(filepath.Join(l.dir, rtype+"s"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", value)
	return err
}

func (l *localnet) leader(port int) (string, error) {
	url := fmt.Sprintf("https://robustirc:%s@localhost:%d/leader", l.NetworkPassword, port)
	resp, err := l.Httpclient.Get(url)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%q: got HTTP %v, expected 200\n", url, resp.Status)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func (l *localnet) Running() bool {
	_, err := os.Stat(filepath.Join(l.dir, "pids"))
	return !os.IsNotExist(err)
}

func (l *localnet) Healthy() bool {
	_, err := util.EnsureNetworkHealthy(l.Servers(), l.NetworkPassword)
	return err == nil
}

func (l *localnet) Servers() []string {
	var addrs []string
	for _, port := range l.Ports {
		addrs = append(addrs, fmt.Sprintf("localhost:%d", port))
	}
	return addrs
}

func (l *localnet) StartIRCServer(singlenode bool) (*exec.Cmd, string, string) {
	args := []string{
		"-network_name=localnet.localhost",
		"-tls_cert_path=" + filepath.Join(l.dir, "cert.pem"),
		"-tls_ca_file=" + filepath.Join(l.dir, "cert.pem"),
		"-tls_key_path=" + filepath.Join(l.dir, "key.pem"),
		"-post_message_cooloff=0",
	}

	args = append(args, fmt.Sprintf("-listen=localhost:%d", l.randomPort))

	// TODO(secure): support -persistent
	tempdir, err := ioutil.TempDir("", "robustirc-localnet-")
	if err != nil {
		log.Fatal(err)
	}
	args = append(args, "-raftdir="+tempdir)
	args = append(args, "-log_dir="+tempdir)
	if err := l.RecordResource("tempdir", tempdir); err != nil {
		log.Panicf("Could not record tempdir: %v", err)
	}

	// Create a shell script with which you can restart a killed robustirc
	// server. This is intentionally before the -singlenode and -join
	// arguments, which are only required for the very first bootstrap.
	f, err := os.OpenFile(filepath.Join(tempdir, "restart.sh"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0755)
	if err != nil {
		log.Panic(err)
	}
	defer f.Close()

	quotedargs := make([]string, len(args))
	for idx, arg := range args {
		quotedargs[idx] = fmt.Sprintf("%q", arg)
	}

	fmt.Fprintf(f, "#!/bin/sh\n")
	fmt.Fprintf(f, "PATH=%q ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND=%s ROBUSTIRC_NETWORK_PASSWORD=%q GOMAXPROCS=%d robustirc %s >>%q 2>>%q\n",
		os.Getenv("PATH"),
		l.EnablePanicCommand,
		l.NetworkPassword,
		runtime.NumCPU(),
		strings.Join(quotedargs, " "),
		filepath.Join(tempdir, "stdout.txt"),
		filepath.Join(tempdir, "stderr.txt"))

	if singlenode {
		args = append(args, "-singlenode")
	} else {
		args = append(args, fmt.Sprintf("-join=localhost:%d", l.Ports[0]))
	}

	log.Printf("Starting %q\n", "robustirc "+strings.Join(args, " "))
	cmd := exec.Command("robustirc", args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ROBUSTIRC_NETWORK_PASSWORD=%s", l.NetworkPassword),
		fmt.Sprintf("GOMAXPROCS=%d", runtime.NumCPU()),
		fmt.Sprintf("ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND=%s", l.EnablePanicCommand))
	stdout, err := os.Create(filepath.Join(tempdir, "stdout.txt"))
	if err != nil {
		log.Panic(err)
	}
	stderr, err := os.Create(filepath.Join(tempdir, "stderr.txt"))
	if err != nil {
		log.Panic(err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Put the robustirc servers into a separate process group, so that they
	// survive when robustirc-localnet terminates.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	if err := cmd.Start(); err != nil {
		log.Panicf("Could not start robustirc: %v", err)
	}
	if err := l.RecordResource("pid", strconv.Itoa(cmd.Process.Pid)); err != nil {
		log.Panicf("Could not record pid: %v", err)
	}

	// Poll the configured listening port to see if the server started up successfully.
	try := 0
	running := false
	for !running && try < 10 {
		_, err := l.Httpclient.Get(fmt.Sprintf("https://localhost:%d/", l.randomPort))
		if err != nil {
			try++
			time.Sleep(250 * time.Millisecond)
			continue
		}

		// Any HTTP response is okay.
		running = true
	}

	if !running {
		cmd.Process.Kill()
		// TODO(secure): retry on a different port.
		log.Fatal("robustirc was not reachable via HTTP after 2.5s")
	}
	l.Ports = append(l.Ports, l.randomPort)

	if singlenode {
		for try := 0; try < 10; try++ {
			leader, err := l.leader(l.Ports[0])
			if err != nil || leader == "" {
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("Server became leader.\n")
			break
		}
	}

	addr := fmt.Sprintf("https://robustirc:%s@localhost:%d/", l.NetworkPassword, l.randomPort)

	log.Printf("Node is available at %s", addr)
	l.randomPort++

	return cmd, tempdir, addr
}

func (l *localnet) StartBridge() {
	var servers []string
	for _, port := range l.Ports {
		servers = append(servers, fmt.Sprintf("localhost:%d", port))
	}

	args := []string{
		"-tls_ca_file=" + filepath.Join(l.dir, "cert.pem"),
		"-network=" + strings.Join(servers, ","),
	}

	tempdir, err := ioutil.TempDir("", "robustirc-bridge-")
	if err != nil {
		log.Fatal(err)
	}
	if err := l.RecordResource("tempdir", tempdir); err != nil {
		log.Panicf("Could not record tempdir: %v", err)
	}

	log.Printf("Starting %q\n", "robustirc-bridge "+strings.Join(args, " "))
	cmd := exec.Command("robustirc-bridge", args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GOMAXPROCS=%d", runtime.NumCPU()))

	stdout, err := os.Create(filepath.Join(tempdir, "stdout.txt"))
	if err != nil {
		log.Panic(err)
	}
	stderr, err := os.Create(filepath.Join(tempdir, "stderr.txt"))
	if err != nil {
		log.Panic(err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Put the robustirc bridge into a separate process group, so that it
	// survives when robustirc-localnet terminates.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	if err := cmd.Start(); err != nil {
		log.Panicf("Could not start robustirc-bridge: %v", err)
	}
	if err := l.RecordResource("pid", strconv.Itoa(cmd.Process.Pid)); err != nil {
		log.Panicf("Could not record pid: %v", err)
	}
}

func (l *localnet) Kill(delete_tempdirs bool) {
	pidsFile := filepath.Join(l.dir, "pids")
	if _, err := os.Stat(pidsFile); os.IsNotExist(err) {
		log.Panicf("-stop specified, but no localnet instance found in -localnet_dir=%q", l.dir)
	}

	pidsBytes, err := ioutil.ReadFile(pidsFile)
	if err != nil {
		log.Panicf("Could not read %q: %v", pidsFile, err)
	}
	pids := strings.Split(string(pidsBytes), "\n")
	for _, pidline := range pids {
		if pidline == "" {
			continue
		}
		pid, err := strconv.Atoi(pidline)
		if err != nil {
			log.Panicf("Invalid line in %q: %v", pidsFile, err)
		}

		process, err := os.FindProcess(pid)
		if err != nil {
			log.Printf("Could not find process %d: %v", pid, err)
			continue
		}
		if err := process.Kill(); err != nil {
			log.Printf("Could not kill process %d: %v", pid, err)
		}
	}

	os.Remove(pidsFile)

	if !delete_tempdirs {
		return
	}

	tempdirsFile := filepath.Join(l.dir, "tempdirs")
	tempdirsBytes, err := ioutil.ReadFile(tempdirsFile)
	if err != nil {
		log.Panicf("Could not read %q: %v", tempdirsFile, err)
	}
	tempdirs := strings.Split(string(tempdirsBytes), "\n")
	for _, tempdir := range tempdirs {
		if tempdir == "" {
			continue
		}

		if err := os.RemoveAll(tempdir); err != nil {
			log.Printf("Could not remove %q: %v", tempdir, err)
		}
	}

	os.Remove(tempdirsFile)
}

func NewLocalnet(port int, dir string) (*localnet, error) {
	result := &localnet{
		dir: dir,
	}

	rand.Seed(time.Now().Unix())

	// Raise the NOFILE soft limit to the hard limit. This is necessary for
	// testing a high number of sessions.
	var rlimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		return nil, err
	}
	rlimit.Cur = rlimit.Max
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		return nil, err
	}

	// (Try to) use a random port in the dynamic port range.
	// NOTE: 55535 instead of 65535 is intentional, so that the
	// startircserver() can increase the port to find a higher unused port.
	if port > -1 {
		result.randomPort = port
	} else {
		result.randomPort = 49152 + rand.Intn(55535-49152)
	}

	result.NetworkPassword = os.Getenv("ROBUSTIRC_NETWORK_PASSWORD")
	if result.NetworkPassword == "" {
		var err error
		result.NetworkPassword, err = randomPassword(20)
		if err != nil {
			return nil, fmt.Errorf("Could not generate password: %v")
		}
	}

	if result.dir[:2] == "~/" {
		usr, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("Cannot expand -localnet_dir: %v", err)
		}
		result.dir = strings.Replace(result.dir, "~/", usr.HomeDir+"/", 1)
	}

	if err := os.MkdirAll(result.dir, 0700); err != nil {
		log.Fatal(err)
	}

	if err := help("robustirc"); err != nil {
		return nil, fmt.Errorf("Could not run %q: %v", "robustirc -help", err)
	}

	if err := help("robustirc-bridge"); err != nil {
		return nil, fmt.Errorf("Could not run %q: %v", "robustirc-bridge -help", err)
	}

	if _, err := os.Stat(filepath.Join(result.dir, "key.pem")); os.IsNotExist(err) {
		generatecert(result.dir)
	}

	roots := x509.NewCertPool()
	contents, err := ioutil.ReadFile(filepath.Join(result.dir, "cert.pem"))
	if err != nil {
		log.Panicf("Could not read cert.pem: %v", err)
	}
	if !roots.AppendCertsFromPEM(contents) {
		log.Panicf("Could not parse %q, try deleting it", filepath.Join(result.dir, "cert.pem"))
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}
	result.Httpclient = &http.Client{Transport: tr}

	// -tls_ca_file is used in util
	flag.Set("tls_ca_file", filepath.Join(result.dir, "cert.pem"))

	return result, nil
}
