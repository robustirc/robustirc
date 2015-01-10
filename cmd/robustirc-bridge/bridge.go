// bridge bridges between IRC clients (RFC1459) and RobustIRC servers.
//
// Bridge instances are supposed to be long-running, and ideally as close to the
// IRC client as possible, e.g. on the same machine. When running on the same
// machine, there should not be any network problems between the IRC client and
// the bridge. Network problems between the bridge and a RobustIRC network are
// handled transparently.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"time"

	"github.com/robustirc/robustirc/cmd/robustirc-bridge/robustsession"

	"github.com/sorcix/irc"
)

var (
	network = flag.String("network",
		"",
		`DNS name to connect to (e.g. "robustirc.net"). The _robustirc._tcp SRV record must be present.`)

	listen = flag.String("listen",
		"localhost:6667",
		"host:port to listen on for IRC connections")

	socks = flag.String("socks", "", "host:port to listen on for SOCKS5 connections")
)

// TODO(secure): persistent state:
// - the last known server(s) in the network. added to *servers
// - for resuming sessions (later): the last seen message id, perhaps setup messages (JOINs, MODEs, …)
// for hosted mode, this state is stored per-nickname, ideally encrypted with password

type bridge struct {
	network string
}

func newBridge(network string) *bridge {
	return &bridge{
		network: network,
	}
}

type ircsession struct {
	Messages chan irc.Message
	Errors   chan error

	conn *irc.Conn
}

func newIrcsession(conn net.Conn) *ircsession {
	s := &ircsession{
		Messages: make(chan irc.Message),
		Errors:   make(chan error),
		conn:     irc.NewConn(conn),
	}
	go s.getMessages()
	return s
}

func (s *ircsession) Send(msg []byte) error {
	if _, err := s.conn.Write(msg); err != nil {
		return err
	}
	return nil
}

func (s *ircsession) Delete(killmsg string) error {
	defer s.conn.Close()

	if killmsg != "" {
		return s.conn.Encode(&irc.Message{
			Command:  "ERROR",
			Trailing: killmsg,
		})
	}

	return nil
}

func (s *ircsession) getMessages() {
	for {
		ircmsg, err := s.conn.Decode()
		if err != nil {
			s.Errors <- err
			return
		}
		// Skip invalid lines (to prevent nil pointer dereferences).
		if ircmsg == nil {
			continue
		}
		log.Printf("<-irc: %q\n", ircmsg.Bytes())
		s.Messages <- *ircmsg
	}
}

func (p *bridge) handleIRC(conn net.Conn) {
	var quitmsg, killmsg string
	var waitingForPingReply bool

	ircSession := newIrcsession(conn)

	defer func() {
		if err := ircSession.Delete(killmsg); err != nil {
			log.Printf("Could not properly delete IRC session: %v\n", err)
		}
	}()

	robustSession, err := robustsession.Create(p.network)
	if err != nil {
		killmsg = fmt.Sprintf("Could not create RobustIRC session: %v", err)
		return
	}

	defer func() {
		log.Printf("deleting robustsession…\n")
		if err := robustSession.Delete(quitmsg); err != nil {
			log.Printf("Could not properly delete RobustIRC session: %v\n", err)
		}
	}()

	var sendIRC, sendRobust []byte

	keepaliveToNetwork := time.After(1 * time.Minute)
	keepaliveToClient := time.After(1 * time.Minute)
	for {
		// These two variables contain the messages to be sent to IRC/RobustIRC
		// from the previous iteration of the state machine. That way, there is
		// only one place where the error handling happens.
		if sendIRC != nil {
			if err := ircSession.Send(sendIRC); err != nil {
				quitmsg = fmt.Sprintf("Bridge: Send to IRC client: %v", err)
				return
			}
			sendIRC = nil
		}
		if sendRobust != nil {
			if err := robustSession.PostMessage(string(sendRobust)); err != nil {
				killmsg = fmt.Sprintf("Could not post message to RobustIRC: %v", err)
				return
			}
			keepaliveToNetwork = time.After(1 * time.Minute)
			keepaliveToClient = time.After(1 * time.Minute)
			sendRobust = nil
		}

		select {
		case msg := <-robustSession.Messages:
			ircmsg := irc.ParseMessage(msg)
			if ircmsg.Command == irc.PONG && len(ircmsg.Params) > 0 && ircmsg.Params[0] == "keepalive" {
				log.Printf("Swallowing keepalive PONG from server to avoid confusing the IRC client.\n")
				break
			}
			sendIRC = []byte(msg)

		case err := <-robustSession.Errors:
			killmsg = fmt.Sprintf("RobustIRC session error: %v", err)
			return

		case ircmsg := <-ircSession.Messages:
			switch ircmsg.Command {
			case irc.PONG:
				waitingForPingReply = false

			case irc.PING:
				sendIRC = (&irc.Message{
					Prefix:  robustSession.IrcPrefix,
					Command: irc.PONG,
					Params:  []string{ircmsg.Params[0]},
				}).Bytes()

			case irc.QUIT:
				quitmsg = ircmsg.Trailing
				return

			default:
				sendRobust = ircmsg.Bytes()
			}

		case err := <-ircSession.Errors:
			quitmsg = fmt.Sprintf("Bridge: Read from IRC client: %v", err)
			return

		case <-keepaliveToClient:
			// After no traffic in either direction for 1 minute, we send a PING
			// message. If a PING message was already sent, this means that we did
			// not receive a PONG message, so we close the connection with a
			// timeout.
			if waitingForPingReply {
				quitmsg = "Bridge: ping timeout"
				return
			}
			sendIRC = (&irc.Message{
				Prefix:  robustSession.IrcPrefix,
				Command: irc.PING,
				Params:  []string{"robustirc.bridge"},
			}).Bytes()
			waitingForPingReply = true

		case <-keepaliveToNetwork:
			sendRobust = []byte("PING keepalive")
			keepaliveToNetwork = time.After(1 * time.Minute)
		}
	}
}

func main() {
	flag.Parse()

	rand.Seed(time.Now().Unix())

	if *network == "" && *socks == "" {
		log.Fatal("You must specify either -network or -socks.")
	}

	// SOCKS and IRC
	if *socks != "" && *network != "" {
		go func() {
			if err := listenAndServeSocks(*socks); err != nil {
				log.Fatal(err)
			}
		}()
	}

	// SOCKS only
	if *socks != "" && *network == "" {
		log.Fatal(listenAndServeSocks(*socks))
	}

	// IRC
	if *network != "" {
		p := newBridge(*network)

		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("RobustIRC IRC bridge listening on %q\n", *listen)

		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("Could not accept IRC client connection: %v\n", err)
				continue
			}
			go p.handleIRC(conn)
		}
	}
}
