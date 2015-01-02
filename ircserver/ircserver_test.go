package ircserver

import (
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
		t.Fatalf("session.Nick: got %q, want %q\n", s.Nick, "secure")
	}
}
