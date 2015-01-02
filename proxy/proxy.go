// proxy bridges between IRC clients (RFC1459) and fancyirc servers.
//
// Proxy instances are supposed to be long-running, and ideally as close to the
// IRC client as possible, e.g. on the same machine. When running on the same
// machine, there should not be any network problems between the IRC client and
// the proxy. Network problems between the proxy and a fancyirc network are
// handled transparently.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fancyirc/types"

	"github.com/sorcix/irc"
)

var (
	servers = flag.String("servers",
		"localhost:8001",
		"(comma-separated) list of host:port network addresses of the server(s) to connect to")

	listen = flag.String("listen",
		"localhost:6667",
		"host:port to listen on for IRC connections")

	currentMaster string
	allServers    []string
)

const (
	pathCreateSession = "/fancyirc/v1/session"
	pathDeleteSession = "/fancyirc/v1/%s"
	pathPostMessage   = "/fancyirc/v1/%s/message"
	pathGetMessages   = "/fancyirc/v1/%s/messages?lastseen=%d"
)

// TODO(secure): persistent state:
// - the last known server(s) in the network. added to *servers
// - for resuming sessions (later): the last seen message id, perhaps setup messages (JOINs, MODEs, …)
// for hosted mode, this state is stored per-nickname, ideally encrypted with password

func sendFancyMessage(logPrefix string, targets []string, path string, data []byte) (*http.Response, error) {
	var (
		resp   *http.Response
		target string
	)
	for {
		var soonest time.Duration
		target = ""
		for target == "" {
			target, soonest = nextCandidate(targets)
			if target == "" {
				log.Printf("%s Waiting %v for back-off time to expire…\n", logPrefix, soonest)
				time.Sleep(soonest)
			}
		}

		log.Printf("%s targets = %v, candidate = %s\n", logPrefix, targets, target)

		var err error
		resp, err = http.Post(
			fmt.Sprintf("http://%s%s", target, path),
			"application/json",
			bytes.NewBuffer(data))

		if err != nil {
			log.Printf("%s %v\n", logPrefix, err)
			serverFailed(target)
			continue
		}

		if resp.StatusCode == http.StatusTemporaryRedirect {
			loc := resp.Header.Get("Location")
			if loc == "" {
				return nil, fmt.Errorf("Redirect has no Location header")
			}
			u, err := url.Parse(loc)
			if err != nil {
				return nil, fmt.Errorf("Could not parse redirection %q: %v", loc, err)
			}

			resp.Body.Close()

			log.Printf("%s %q redirects us to %q\n", logPrefix, target, u.Host)

			// Even though the server did not actually fail, it did not answer our
			// request either. To prevent hammering it, mark it as failed for
			// back-off purposes.
			serverFailed(target)
			targets = append([]string{u.Host}, targets...)
			continue
		}

		if resp.StatusCode != 200 {
			data, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("%s sendFancyMessage(%q) failed with %v: %s", logPrefix, path, resp.Status, string(data))
			serverFailed(target)
			continue
		}

		break
	}
	log.Printf("%s ->fancy: %q\n", logPrefix, string(data))

	currentMaster = target
	return resp, nil
}

func sendIRCMessage(logPrefix string, ircConn *irc.Conn, msg irc.Message) {
	if err := ircConn.Encode(&msg); err != nil {
		log.Printf("%s Error sending IRC message %q: %v. Closing connection.\n", logPrefix, msg.Bytes(), err)
		// This leads to an error in .Decode(), terminating the handleIRC goroutine.
		ircConn.Close()
		return
	}
	log.Printf("%s ->irc: %q\n", logPrefix, msg.Bytes())
}

func createFancySession(logPrefix string) (session string, prefix irc.Prefix, err error) {
	var resp *http.Response
	targets := append([]string{currentMaster}, allServers...)
	resp, err = sendFancyMessage(logPrefix, targets, pathCreateSession, []byte{})
	if err != nil {
		return
	}
	defer resp.Body.Close()

	type createSessionReply struct {
		Sessionid string
		Prefix    string
	}

	var createreply createSessionReply

	if err = json.NewDecoder(resp.Body).Decode(&createreply); err != nil {
		return
	}

	session = createreply.Sessionid
	prefix = irc.Prefix{Name: createreply.Prefix}
	return
}

func handleIRC(conn net.Conn) {
	var (
		logPrefix     = conn.RemoteAddr().String()
		ircConn       = irc.NewConn(conn)
		ircErrors     = make(chan error)
		ircMessages   = make(chan irc.Message)
		fancyMessages = make(chan string)

		ircPrefix irc.Prefix
		session   string
		quitmsg   string
		done      bool
		pingSent  bool
		err       error
	)

	session, ircPrefix, err = createFancySession(logPrefix)
	if err != nil {
		log.Printf("%s Could not create fancyirc session: %v\n", logPrefix, err)
		sendIRCMessage(logPrefix, ircConn, irc.Message{
			Command:  "ERROR",
			Trailing: fmt.Sprintf("Could not create fancyirc session: %v", err),
		})

		ircConn.Close()
		return
	}

	go func() {
		for {
			message, err := ircConn.Decode()
			if err != nil {
				ircErrors <- err
				return
			}
			log.Printf("%s <-irc: %q\n", logPrefix, message.Bytes())
			ircMessages <- *message
		}
	}()

	// TODO(secure): periodically get all the servers in the network, overwrite allServers (so that deletions work)

	go func() {
		var lastSeen types.FancyId

		for !done {
			host, resp := getMessages(logPrefix, session, lastSeen)

			// We set the host as currentMaster, not because the host is the
			// master, but because it is reachable. When sending messages, we will
			// either reach the master by chance or get redirected, at which point
			// we update currentMaster.
			currentMaster = host

			dec := json.NewDecoder(resp.Body)
			for !done {
				// TODO(secure): we need a ping message here as well, so that we can detect timeouts quickly. It could include the current servers.
				var msg types.FancyMessage
				if err := dec.Decode(&msg); err != nil {
					log.Printf("%s Protocol error on %q: Could not decode response chunk as JSON: %v\n", logPrefix, host, err)
					break
				}

				log.Printf("%s <-fancy: %q\n", logPrefix, msg.Data)
				fancyMessages <- msg.Data
				lastSeen = msg.Id
			}
			resp.Body.Close()
		}

		close(fancyMessages)

		log.Printf("Disconnecting.\n")
	}()

	// Read all remaining messages to prevent goroutine hangs.
	defer func() {
		for _ = range fancyMessages {
		}

		log.Printf("TODO: close session with msg %q\n", quitmsg)
		// TODO: close the session on the server
	}()

	for {
		select {
		case <-time.After(1 * time.Minute):
			// After no traffic in either direction for 1 minute, we send a PING
			// message. If a PING message was already sent, this means that we did
			// not receive a PONG message, so we close the connection with at
			// timeout.
			if pingSent {
				quitmsg = "ping timeout"
				ircConn.Close()
			} else {
				sendIRCMessage(logPrefix, ircConn, irc.Message{
					Prefix:  &ircPrefix,
					Command: irc.PING,
					Params:  []string{"fancyirc.proxy"},
				})
			}

		case err := <-ircErrors:
			log.Printf("Error in IRC client connection: %v\n", err)
			done = true
			return

		case msg := <-fancyMessages:
			if _, err := fmt.Fprintf(conn, "%s\n", msg); err != nil {
				log.Fatal(err)
			}

		case message := <-ircMessages:
			switch message.Command {
			case irc.PONG:
				log.Printf("%s received PONG reply.\n", logPrefix)
				pingSent = false
			case irc.PING:
				sendIRCMessage(logPrefix, ircConn, irc.Message{
					Prefix:  &ircPrefix,
					Command: irc.PONG,
					Params:  message.Params,
				})
			case irc.QUIT:
				quitmsg = message.Trailing
			default:
				targets := append([]string{currentMaster}, allServers...)
				resp, err := sendFancyMessage(logPrefix, targets, fmt.Sprintf(pathPostMessage, session), message.Bytes())
				if err != nil {
					// TODO(secure): what should we do here?
					log.Printf("message could not be sent: %v\n", err)
				}
				resp.Body.Close()
			}
		}
	}
}

func main() {
	flag.Parse()

	rand.Seed(time.Now().Unix())

	// Start with any server. Will be overwritten later.
	allServers = strings.Split(*servers, ",")
	if len(allServers) == 0 {
		log.Fatalf("Invalid -servers value (%q). Need at least one server.\n", *servers)
	}
	currentMaster = allServers[0]

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("fancyirc proxy listening on %q\n", *listen)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Could not accept IRC client connection: %v\n", err)
			continue
		}
		go handleIRC(conn)
	}
}
