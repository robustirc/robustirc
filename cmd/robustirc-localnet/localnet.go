// robustirc-localnet starts 3 RobustIRC servers on localhost on random ports
// with temporary data directories, generating a self-signed SSL certificate.
// stdout and stderr are redirected to a file in the temporary data directory
// of each node.
//
// robustirc-localnet can be used for playing around with RobustIRC, especially
// when developing.
package main

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
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	localnetDir = flag.String("localnet_dir",
		"~/.config/robustirc-localnet",
		"Directory in which to keep state for robustirc-localnet (SSL certificates, PID files, etc.)")

	stop = flag.Bool("stop",
		false,
		"Whether to stop the currently running localnet instead of starting a new one")

	delete_tempdirs = flag.Bool("delete_tempdirs",
		true,
		"If false, temporary directories are left behind for manual inspection")
)

var (
	randomPort      int
	networkPassword string

	// An http.Client which has the generated SSL certificate in its list of root CAs.
	httpclient *http.Client

	// List of ports on which the RobustIRC servers are running on.
	ports []int
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

// recordResource appends a line to a file in -localnet_dir so that we can
// clean up resources (tempdirs, pids) when being called with -stop later.
func recordResource(rtype string, value string) error {
	f, err := os.OpenFile(filepath.Join(*localnetDir, rtype+"s"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", value)
	return err
}

func leader(port int) (string, error) {
	url := fmt.Sprintf("https://robustirc:%s@localhost:%d/leader", networkPassword, port)
	resp, err := httpclient.Get(url)
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

func startircserver(singlenode bool) {
	args := []string{
		"-network_name=localnet.localhost",
		"-tls_cert_path=" + filepath.Join(*localnetDir, "cert.pem"),
		"-tls_ca_file=" + filepath.Join(*localnetDir, "cert.pem"),
		"-tls_key_path=" + filepath.Join(*localnetDir, "key.pem"),
		"-post_message_cooloff=0",
	}

	args = append(args, fmt.Sprintf("-listen=localhost:%d", randomPort))

	// TODO(secure): support -persistent
	tempdir, err := ioutil.TempDir("", "robustirc-localnet-")
	if err != nil {
		log.Fatal(err)
	}
	args = append(args, "-raftdir="+tempdir)
	args = append(args, "-log_dir="+tempdir)
	if err := recordResource("tempdir", tempdir); err != nil {
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
	fmt.Fprintf(f, "PATH=%q robustirc %s >>%q 2>>%q\n",
		os.Getenv("PATH"),
		strings.Join(quotedargs, " "),
		filepath.Join(tempdir, "stdout.txt"),
		filepath.Join(tempdir, "stderr.txt"))

	if singlenode {
		args = append(args, "-singlenode")
	} else {
		args = append(args, fmt.Sprintf("-join=localhost:%d", ports[0]))
	}

	log.Printf("Starting %q\n", "robustirc "+strings.Join(args, " "))
	cmd := exec.Command("robustirc", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("ROBUSTIRC_NETWORK_PASSWORD=%s", networkPassword))
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
	if err := recordResource("pid", strconv.Itoa(cmd.Process.Pid)); err != nil {
		log.Panicf("Could not record pid: %v", err)
	}

	// Poll the configured listening port to see if the server started up successfully.
	try := 0
	running := false
	for !running && try < 10 {
		_, err := httpclient.Get(fmt.Sprintf("https://localhost:%d/", randomPort))
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
	ports = append(ports, randomPort)

	if singlenode {
		for try := 0; try < 10; try++ {
			leader, err := leader(ports[0])
			if err != nil || leader == "" {
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("Server became leader.\n")
			break
		}
	}

	log.Printf("Node is available at https://robustirc:%s@localhost:%d/", networkPassword, randomPort)
	randomPort++
}

func startbridge() {
	var servers []string
	for _, port := range ports {
		servers = append(servers, fmt.Sprintf("localhost:%d", port))
	}

	args := []string{
		"-tls_ca_file=" + filepath.Join(*localnetDir, "cert.pem"),
		"-network=" + strings.Join(servers, ","),
	}

	tempdir, err := ioutil.TempDir("", "robustirc-bridge-")
	if err != nil {
		log.Fatal(err)
	}
	if err := recordResource("tempdir", tempdir); err != nil {
		log.Panicf("Could not record tempdir: %v", err)
	}

	log.Printf("Starting %q\n", "robustirc-bridge "+strings.Join(args, " "))
	cmd := exec.Command("robustirc-bridge", args...)

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
	if err := recordResource("pid", strconv.Itoa(cmd.Process.Pid)); err != nil {
		log.Panicf("Could not record pid: %v", err)
	}
}

func kill() {
	pidsFile := filepath.Join(*localnetDir, "pids")
	if _, err := os.Stat(pidsFile); os.IsNotExist(err) {
		log.Panicf("-stop specified, but no localnet instance found in -localnet_dir=%q", *localnetDir)
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

	if !*delete_tempdirs {
		return
	}

	tempdirsFile := filepath.Join(*localnetDir, "tempdirs")
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

func main() {
	flag.Parse()

	rand.Seed(time.Now().Unix())

	// (Try to) use a random port in the dynamic port range.
	// NOTE: 55535 instead of 65535 is intentional, so that the
	// startircserver() can increase the port to find a higher unused port.
	randomPort = 49152 + rand.Intn(55535-49152)

	networkPassword = os.Getenv("ROBUSTIRC_NETWORK_PASSWORD")
	if networkPassword == "" {
		var err error
		networkPassword, err = randomPassword(20)
		if err != nil {
			log.Fatalf("Could not generate password: %v")
		}
	}

	if (*localnetDir)[:2] == "~/" {
		usr, err := user.Current()
		if err != nil {
			log.Panicf("Cannot expand -localnet_dir: %v", err)
		}
		*localnetDir = strings.Replace(*localnetDir, "~/", usr.HomeDir+"/", 1)
	}

	if err := os.MkdirAll(*localnetDir, 0700); err != nil {
		log.Fatal(err)
	}

	if *stop {
		kill()
		return
	}

	if _, err := os.Stat(filepath.Join(*localnetDir, "pids")); !os.IsNotExist(err) {
		log.Panicf("There already is a localnet instance running. Either use -stop or specify a different -localnet_dir")
	}

	success := false

	defer func() {
		if success {
			return
		}
		log.Printf("Could not successfully set up localnet, cleaning up.\n")
		kill()
	}()

	if err := help("robustirc"); err != nil {
		log.Panicf("Could not run %q: %v", "robustirc -help", err)
	}

	if err := help("robustirc-bridge"); err != nil {
		log.Panicf("Could not run %q: %v", "robustirc-bridge -help", err)
	}

	if _, err := os.Stat(filepath.Join(*localnetDir, "key.pem")); os.IsNotExist(err) {
		generatecert()
	}

	roots := x509.NewCertPool()
	contents, err := ioutil.ReadFile(filepath.Join(*localnetDir, "cert.pem"))
	if err != nil {
		log.Panicf("Could not read cert.pem: %v", err)
	}
	if !roots.AppendCertsFromPEM(contents) {
		log.Panicf("Could not parse %q, try deleting it", filepath.Join(*localnetDir, "cert.pem"))
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}
	httpclient = &http.Client{Transport: tr}

	startircserver(true)
	startircserver(false)
	startircserver(false)
	startbridge()

	try := 0
	for try < 10 {
		try++

		leaders := make([]string, len(ports))
		for idx, port := range ports {
			l, err := leader(port)
			if err != nil {
				log.Printf("%v\n", err)
				continue
			}
			leaders[idx] = l
		}

		if leaders[0] == "" {
			log.Printf("No leader established yet.\n")
			time.Sleep(1 * time.Second)
			continue
		}

		if leaders[0] != leaders[1] || leaders[0] != leaders[2] {
			log.Printf("Leader not the same on all servers.\n")
			time.Sleep(1 * time.Second)
			continue
		}

		if strings.HasPrefix(leaders[0], "localhost:") {
			log.Printf("All nodes agree on %q as the leader.\n", leaders[0])
			success = true
			break
		}
	}
}
