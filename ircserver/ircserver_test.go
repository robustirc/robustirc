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

	s, err := GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	ProcessMessage(id, irc.ParseMessage("NICK secure"))
	ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	if s.Nick != "secure" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}

	if !s.loggedIn() {
		t.Fatalf("session.loggedIn() still false after sending NICK and USER")
	}
}

func TestNickCollision(t *testing.T) {
	var got, want []*irc.Message

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
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 433 * s[E]CuRE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("NICK S[E]CURE"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 433 * S[E]CURE :Nickname is already in use.")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("NICK S{E}CURE"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 433 * S{E}CURE :Nickname is already in use.")}
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

	s, err := GetSession(id)
	if err != nil {
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

	s, err := GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	ProcessMessage(id, irc.ParseMessage("NICK secure"))
	ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	got := ProcessMessage(id, irc.ParseMessage("JOIN #foobar"))
	want := []*irc.Message{
		&irc.Message{Prefix: &s.ircPrefix, Command: irc.JOIN, Trailing: "#foobar"},
		irc.ParseMessage(":robustirc.net 331 secure #foobar :No topic is set"),
		irc.ParseMessage(":robustirc.net 353 secure = #foobar :secure"),
		irc.ParseMessage(":robustirc.net 366 secure #foobar :End of /NAMES list."),
	}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(id, irc.ParseMessage("JOIN foobar"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 403 secure foobar :No such channel")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestInvalidPrivmsg(t *testing.T) {
	id := types.RobustId{Id: time.Now().UnixNano()}

	ClearState()
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}
	CreateSession(id, "authbytes")

	ProcessMessage(id, irc.ParseMessage("NICK secure"))
	ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	ProcessMessage(id, irc.ParseMessage("JOIN #test"))
	got := ProcessMessage(id, irc.ParseMessage("PRIVMSG #test"))
	want := []irc.Message{*irc.ParseMessage(":robustirc.net 412 secure :No text to send")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(id, irc.ParseMessage("PRIVMSG #test foo"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 412 secure :No text to send")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(id, irc.ParseMessage("PRIVMSG"))
	want = []irc.Message{*irc.ParseMessage(":robustirc.net 411 secure :No recipient given (PRIVMSG)")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestKill(t *testing.T) {
	var got, want []*irc.Message

	ClearState()
	NetworkPassword = "foo"
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}
	idMero := types.RobustId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	CreateSession(idMero, "auth-mero")

	got = ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	ProcessMessage(idSecure, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	if len(got) > 0 {
		for _, msg := range got {
			if msg.Command != irc.ERR_NICKNAMEINUSE {
				continue
			}
			t.Fatalf("got %v, wanted anything but ERR_NICKNAMEINUSE", msg)
		}
	}
	got = ProcessMessage(idMero, irc.ParseMessage("NICK mero"))
	ProcessMessage(idMero, irc.ParseMessage("USER foo 0 * :Axel Wagner"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 001 mero :Welcome to RobustIRC!")}
	if len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got[0], want[0])
	}

	got = ProcessMessage(idMero, irc.ParseMessage("KILL secure"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 461 mero KILL :Not enough parameters")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("KILL"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 461 mero KILL :Not enough parameters")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("KILL secure :die"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 481 mero :Permission Denied - You're not an IRC operator")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("OPER mero bar"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 464 mero :Password incorrect")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("OPER mero foo"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 381 mero :You are now an IRC operator")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("KILL socoro :die"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 401 mero socoro :No such nick/channel")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("KILL s[E]CuRE :die now, will you?"))
	want = []*irc.Message{irc.ParseMessage(":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: die now, will you?")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	if _, err := GetSession(idSecure); err == nil {
		t.Fatalf("GetSession(%v) returned a session after KILL", idSecure)
	}
}

func TestAway(t *testing.T) {
	var got, want []*irc.Message

	ClearState()
	NetworkPassword = "foo"
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}
	idMero := types.RobustId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	CreateSession(idMero, "auth-mero")

	ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	ProcessMessage(idSecure, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	ProcessMessage(idMero, irc.ParseMessage("NICK mero"))
	ProcessMessage(idMero, irc.ParseMessage("USER foo 0 * :Axel Wagner"))

	got = ProcessMessage(idSecure, irc.ParseMessage("PRIVMSG mero :hey"))
	want = []*irc.Message{irc.ParseMessage(":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :hey")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("AWAY :upgrading server"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 306 mero :You have been marked as being away")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idSecure, irc.ParseMessage("PRIVMSG mero :you there?"))
	want = []*irc.Message{
		irc.ParseMessage(":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there?"),
		irc.ParseMessage(":robustirc.net 301 s[E]CuRE mero :upgrading server"),
	}
	if len(got) != len(want) || len(got) < 2 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 || bytes.Compare(got[1].Bytes(), want[1].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("AWAY"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 305 mero :You are no longer marked as being away")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestTopic tests that setting/getting topics works properly. Using topic as
// the channel’s state, it also verifies that joining/parting will
// create/delete channels.
func TestTopic(t *testing.T) {
	var got, want []*irc.Message

	ClearState()
	NetworkPassword = "foo"
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}
	idMero := types.RobustId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	sSecure, _ := GetSession(idSecure)
	CreateSession(idMero, "auth-mero")
	sMero, _ := GetSession(idMero)

	ProcessMessage(idSecure, irc.ParseMessage("NICK sECuRE"))
	ProcessMessage(idSecure, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	ProcessMessage(idSecure, irc.ParseMessage("JOIN #test"))
	ProcessMessage(idMero, irc.ParseMessage("NICK mero"))
	ProcessMessage(idMero, irc.ParseMessage("USER foo 0 * :Axel Wagner"))

	got = ProcessMessage(idSecure, irc.ParseMessage("TOPIC #nonexistant"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 403 sECuRE #nonexistant :No such channel")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :yeah, this is a topic."))
	want = []*irc.Message{&irc.Message{
		Prefix:   &sSecure.ircPrefix,
		Command:  irc.TOPIC,
		Params:   []string{"#test"},
		Trailing: "yeah, this is a topic."},
	}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :"))
	want = []*irc.Message{irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :yeah, this is a topic."))

	got = ProcessMessage(idMero, irc.ParseMessage("JOIN #test"))
	want = []*irc.Message{
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
		irc.ParseMessage(":robustirc.net 332 sECuRE #test :yeah, this is a topic."),
		irc.ParseMessage(":robustirc.net 333 sECuRE #test sECuRE 1421864845"),
		irc.ParseMessage(":robustirc.net 353 mero = #test :sECuRE mero"),
		irc.ParseMessage(":robustirc.net 366 mero #test :End of /NAMES list."),
	}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("PART #test"))
	want = []*irc.Message{&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}}}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("TOPIC #test"))
	want = []*irc.Message{
		irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
		irc.ParseMessage(":robustirc.net 333 mero #test sECuRE 1421864845"),
	}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idSecure, irc.ParseMessage("PART #test"))
	want = []*irc.Message{&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.PART, Params: []string{"#test"}}}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = ProcessMessage(idMero, irc.ParseMessage("TOPIC #test"))
	want = []*irc.Message{irc.ParseMessage(":robustirc.net 403 mero #test :No such channel")}
	if len(got) != len(want) || len(got) < 1 || bytes.Compare(got[0].Bytes(), want[0].Bytes()) != 0 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMotd(t *testing.T) {
	var got []*irc.Message

	ClearState()
	NetworkPassword = "foo"
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}

	CreateSession(idSecure, "auth-secure")

	got = ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	motdFound := false
	for i := 0; i < len(got)-2; i++ {
		if got[i].Command == irc.RPL_MOTDSTART &&
			got[i+1].Command == irc.RPL_MOTD &&
			got[i+2].Command == irc.RPL_ENDOFMOTD {
			motdFound = true
			break
		}
	}
	if !motdFound {
		t.Fatalf("got %v, did not find MOTDSTART, MOTD, ENDOFMOTD in order", got)
	}
}
