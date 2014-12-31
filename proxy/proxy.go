package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/sorcix/irc"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	servers = flag.String("servers",
		"localhost:8001",
		"(comma-separated) list of host:port network addresses of the server(s) to connect to")

	listen = flag.String("listen",
		"localhost:6667",
		"host:port to listen on for IRC connections")

	currentMaster string

	fromFancyMsgs = make(chan string)
)

// TODO: move this to a common package, perhaps fancyirc/types?
type fancyId int64
type fancyMessage struct {
	Id   fancyId
	Data string
}

// TODO(secure): persistent state:
// - the follow targets (channels)
// - the last known server(s) in the network. added to *servers
// - the last seen message id
// for hosted mode, this state is stored per-nickname, ideally encrypted with password

func sendMessage(master string, data []byte) error {
	res, err := http.Post(
		fmt.Sprintf("http://%s/put", master),
		"application/json",
		bytes.NewBuffer(data))

	if err != nil {
		return err
	}

	if res.StatusCode > 399 {
		data, _ := ioutil.ReadAll(res.Body)
		return fmt.Errorf("sendMessage() failed: %s", string(data))
	}

	if res.StatusCode > 299 {
		loc := res.Header.Get("Location")
		if loc == "" {
			return fmt.Errorf("Redirect has no Location header")
		}
		u, err := url.Parse(loc)
		if err != nil {
			return fmt.Errorf("Could not parse redirection %q: %v", loc, err)
		}

		return sendMessage(u.Host, data)
	}

	currentMaster = master
	return nil
}

func handleIRC(conn net.Conn) {
	// TODO(secure): defer closing the session on the server.

	// TODO(secure): use proper host name
	prefix := irc.Prefix{Name: "proxy.fancyirc"}
	ircConn := irc.NewConn(conn)
	nick := ""
	reply := func(msg irc.Message) {
		msg.Prefix = &prefix
		log.Printf("Replying: %+v\n", msg)
		if err := ircConn.Encode(&msg); err != nil {
			log.Printf("Error sending IRC message: %v. Closing connection.\n", err)
			// This leads to an error in .Decode(), terminating this goroutine.
			ircConn.Close()
		}
	}
	ircErrors := make(chan error)
	ircMessages := make(chan irc.Message)

	go func() {
		for {
			message, err := ircConn.Decode()
			if err != nil {
				ircErrors <- err
				return
			}
			ircMessages <- *message
		}
	}()
	for {
		select {
		case err := <-ircErrors:
			log.Printf("Error in IRC client connection: %v\n", err)
			return

		case b := <-fromFancyMsgs:
			if _, err := conn.Write([]byte(b)); err != nil {
				log.Fatal(err)
			}
			if _, err := conn.Write([]byte("\n")); err != nil {
				log.Fatal(err)
			}

		case message := <-ircMessages:
			log.Printf("got: %+v (command = %s, params = %v, trailing = %s)\n", message, message.Command, message.Params, message.Trailing)

			// TODO(secure): send everything to the server, except for PING messages.

			switch message.Command {
			case irc.PING:
				reply(irc.Message{Command: irc.PONG, Params: message.Params})
			case irc.NICK:
				log.Printf("requested nickname is %q\n", message.Params[0])
				nick = message.Params[0]
				// TODO(secure): this needs to create a session on the IRC server.
				// TODO(secure): figure out whether we want to have at most 1 irc connection per nickname/session.
			case irc.USER:
				// TODO(secure): the irc server, not the proxy, is supposed to send these messages
				// TODO(secure): send 002, 003, 004, 005, 251, 252, 254, 255, 265, 266, [motd = 375, 372, 376]
				reply(irc.Message{
					Command:  irc.RPL_WELCOME,
					Params:   []string{nick},
					Trailing: "Welcome to fancyirc :)",
				})
			case irc.PRIVMSG:
				// TODO: we need to associate a session with this.
				if err := sendMessage(currentMaster, message.Bytes()); err != nil {
					// TODO(secure): what should we do here?
					log.Printf("message could not be sent: %v\n", err)
				}

			default:
			}
		}
	}
}

func main() {
	flag.Parse()

	log.Printf("proxy initializing\n")

	rand.Seed(time.Now().Unix())

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("Could not accept IRC client connection: %v\n", err)
				continue
			}
			go handleIRC(conn)
		}
	}()

	// TODO(secure): periodically get all the servers in the network, overwrite allServers (so that deletions work)
	allServers := strings.Split(*servers, ",")

	var lastSeen fancyId

	for {
		host := allServers[rand.Intn(len(allServers))]
		// TODO(secure): exponential backoff in a per-server fashion
		log.Printf("Connecting to %q...\n", host)
		// TODO(secure): build targets (= filters) and add them to the url
		hostUrl := "http://" + host + "/follow"
		u, err := url.Parse(hostUrl)
		if err != nil {
			log.Fatalf("Could not parse URL %q: %v. Check -servers?\n", hostUrl, err)
		}
		if lastSeen > 0 {
			query := u.Query()
			query.Set("lastseen", strconv.FormatInt(int64(lastSeen), 10))
			u.RawQuery = query.Encode()
		}

		resp, err := http.Get(u.String())
		if err != nil {
			log.Printf("HTTP GET on %q failed: %v\n", host, err)
			continue
		}

		if resp.StatusCode != 200 {
			log.Printf("Received unexpected status code from %q: %v\n", host, resp.Status)
			continue
		}

		// We set the host as currentMaster, not because the host is the
		// master, but because it is reachable. When sending messages, we will
		// either reach the master by chance or get redirected, at which point
		// we update currentMaster.
		currentMaster = host

		dec := json.NewDecoder(resp.Body)
		for {
			// TODO(secure): cancel this loop on JOINs so that the new filter becomes effective
			var msg fancyMessage
			if err := dec.Decode(&msg); err != nil {
				log.Printf("Protocol error on %q: Could not decode response chunk as JSON: %v\n", host, err)
				break
			}

			log.Printf("received: %+v\n", msg)
			// TODO(secure): loop through all connected clients and send messages to every client which is interested.
			fromFancyMsgs <- msg.Data
			lastSeen = msg.Id
		}
		resp.Body.Close()
	}
}
