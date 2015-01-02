package ircserver

import (
	"bytes"
	"testing"
	"time"

	"fancyirc/types"

	"github.com/sorcix/irc"
)

func TestSessionInitialization(t *testing.T) {
	id := types.FancyId{Id: time.Now().UnixNano()}

	CreateSession(id, "authbytes")

	s, ok := GetSession(id)
	if !ok {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	ProcessMessage(id, irc.ParseMessage("NICK secure"))

	if s.Nick != "secure" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}
}

func TestNickCollision(t *testing.T) {
	var got, want []irc.Message

	ClearState()

	idSecure := types.FancyId{Id: 1420228218166687917}
	idMero := types.FancyId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	CreateSession(idMero, "auth-mero")

	got = ProcessMessage(idSecure, irc.ParseMessage("NICK sECuRE"))
	if len(got) > 0 {
		for _, msg := range got {
			if msg.Command != irc.ERR_NICKNAMEINUSE {
				continue
			}
			t.Fatalf("got %v, wanted anything but ERR_NICKNAMEINUSE", msg)
		}
	}
	got = ProcessMessage(idMero, irc.ParseMessage("NICK sECuRE"))
	want = []irc.Message{*irc.ParseMessage(":fancy.twice-irc.de 433 * sECuRE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("NICK SECURE"))
	want = []irc.Message{*irc.ParseMessage(":fancy.twice-irc.de 433 * SECURE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}
