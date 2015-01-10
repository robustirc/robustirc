package ircserver

import (
	"bytes"
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

func TestSessionInitialization(t *testing.T) {
	id := types.RobustId{Id: time.Now().UnixNano()}

	CreateSession(id, "authbytes")
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

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
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}
	idMero := types.RobustId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	CreateSession(idMero, "auth-mero")

	got = ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	if len(got) > 0 {
		for _, msg := range got {
			if msg.Command != irc.ERR_NICKNAMEINUSE {
				continue
			}
			t.Fatalf("got %v, wanted anything but ERR_NICKNAMEINUSE", msg)
		}
	}
	got = ProcessMessage(idMero, irc.ParseMessage("NICK s[E]CuRE"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 433 * s[E]CuRE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("NICK S[E]CURE"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 433 * S[E]CURE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("NICK S{E}CURE"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 433 * S{E}CURE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestInvalidNick(t *testing.T) {
	validNicks := []string{
		"secure",
		"[nn]secure",
		"[nn]-secure",
		"`secure",
		"^secure",
		"^",
		"^0",
		"^0-",
		"^^0secure",
	}

	invalidNicks := []string{
		"0secure",
		"-secure",
		"0",
		"secöre",
	}

	for _, nick := range validNicks {
		if !IsValidNickname(nick) {
			t.Fatalf("IsValidNickname(%q): got %v, want %v", nick, false, true)
		}
	}

	for _, nick := range invalidNicks {
		if IsValidNickname(nick) {
			t.Fatalf("IsValidNickname(%q): got %v, want %v", nick, true, false)
		}
	}
}

func TestInvalidNickPlumbing(t *testing.T) {
	id := types.RobustId{Id: time.Now().UnixNano()}

	ClearState()
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}
	CreateSession(id, "authbytes")

	s, ok := GetSession(id)
	if !ok {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	got := ProcessMessage(id, irc.ParseMessage("NICK 0secure"))
	want := []irc.Message{*irc.ParseMessage(":robustirc.net 432 * 0secure :Erroneus nickname.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	if s.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
}

func TestInvalidChannelPlumbing(t *testing.T) {
	id := types.RobustId{Id: time.Now().UnixNano()}

	ClearState()
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}
	CreateSession(id, "authbytes")

	s, ok := GetSession(id)
	if !ok {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	ProcessMessage(id, irc.ParseMessage("NICK secure"))

	got := ProcessMessage(id, irc.ParseMessage("JOIN #foobar"))
	want := []irc.Message{
		irc.Message{
			Prefix:   s.ircPrefix(),
			Command:  irc.JOIN,
			Trailing: "#foobar",
		},
		*irc.ParseMessage(":robustirc.net 353 secure = #foobar :secure"),
		*irc.ParseMessage(":robustirc.net 366 secure #foobar :End of /NAMES list."),
	}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(id, irc.ParseMessage("JOIN foobar"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 403 secure foobar :No such channel")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}
