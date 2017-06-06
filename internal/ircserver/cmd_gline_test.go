package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestGline(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("OPER mero foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 mero :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE mero :+o"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("GLINE secure :bye")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net NOTICE mero :Cannot kill sECuRE: no IP address known yet"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"], RemoteAddr: "192.168.1.2"}, irc.ParseMessage("PING"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("GLINE secure :bye")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: bye"),
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae KILL sECuRE :ircd!robust/0x13b5aa0a2bcfb8ae!mero (bye)"),
			irc.ParseMessage("ERROR :Closing Link: sECuRE[robust/0x13b5aa0a2bcfb8ad] (Killed (mero (bye)))"),
		})

	// test that a new session cannot be established with the same IP address
	id := robust.Id{Id: 1420228218166687919}
	i.CreateSession(id, "authbytes", time.Unix(0, int64(id.Id)))

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: id, RemoteAddr: "192.168.1.2"}, irc.ParseMessage("NICK attacker")),
		[]*irc.Message{
			irc.ParseMessage("ERROR :Closing Link: You are banned (bye)"),
		})
}
