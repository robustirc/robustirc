package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestNickCollision(t *testing.T) {
	var got *Replyctx

	i, _ := stdIRCServer()

	idSecure := types.RobustId{Id: 1420228218166687333}
	idMero := types.RobustId{Id: 1420228218166687444}

	i.CreateSession(idSecure, "auth-secure")
	i.CreateSession(idMero, "auth-mero")

	got = i.ProcessMessage(types.RobustId{}, idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	if len(got.Messages) > 0 {
		for _, msg := range got.Messages {
			if irc.ParseMessage(msg.Data).Command != irc.ERR_NICKNAMEINUSE {
				continue
			}
			t.Fatalf("got %v, wanted anything but ERR_NICKNAMEINUSE", msg)
		}
	}

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, idMero, irc.ParseMessage("NICK s[E]CuRE")),
		":robustirc.net 433 * s[E]CuRE :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, idMero, irc.ParseMessage("NICK S[E]CURE")),
		":robustirc.net 433 * S[E]CURE :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, idMero, irc.ParseMessage("NICK S{E}CURE")),
		":robustirc.net 433 * S{E}CURE :Nickname is already in use")
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
	i, _ := stdIRCServer()

	id := types.RobustId{Id: time.Now().UnixNano()}
	i.CreateSession(id, "authbytes")

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, id, irc.ParseMessage("NICK 0secure")),
		":robustirc.net 432 * 0secure :Erroneous nickname")

	if s.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
}