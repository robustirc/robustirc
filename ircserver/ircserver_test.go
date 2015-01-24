package ircserver

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

// mustMatchIrcmsgs compares two slices of irc.Messages and logs the contents
// before failing the test if they don’t match byte for byte:
//
// --- FAIL: TestAway (0.00s)
// 	ircserver_test.go:285: got (2 messages):
// 	ircserver_test.go:287:     :s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there?
// 	ircserver_test.go:287:     :robustirc.net 301 s[E]CuRE mero :upgrading server
// 	ircserver_test.go:289: want (2 messages):
// 	ircserver_test.go:291:     :s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :yo there?
// 	ircserver_test.go:291:     :robustirc.net 301 s[E]CuRE mero :upgrading server
// 	ircserver_test.go:293: ProcessMessage() return value does not match expectation: got [:s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there? :robustirc.net 301 s[E]CuRE mero :upgrading server], want [:s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :yo there? :robustirc.net 301 s[E]CuRE mero :upgrading server]
func mustMatchIrcmsgs(t *testing.T, got []*irc.Message, want []*irc.Message) {
	failed := len(got) != len(want)
	for idx := 0; !failed && idx < len(want); idx++ {
		failed = bytes.Compare(got[idx].Bytes(), want[idx].Bytes()) != 0
	}
	if failed {
		t.Logf("got (%d messages):\n", len(got))
		for _, msg := range got {
			t.Logf("    %s\n", msg.Bytes())
		}
		t.Logf("want (%d messages):\n", len(want))
		for _, msg := range want {
			t.Logf("    %s\n", msg.Bytes())
		}
		t.Fatalf("ProcessMessage() return value does not match expectation: got %v, want %v", got, want)
	}
}

func mustMatchIrcmsg(t *testing.T, got []*irc.Message, want *irc.Message) {
	mustMatchIrcmsgs(t, got, []*irc.Message{want})
}

func mustMatchMsg(t *testing.T, got []*irc.Message, want string) {
	mustMatchIrcmsgs(t, got, []*irc.Message{irc.ParseMessage(want)})
}

func TestSessionInitialization(t *testing.T) {
	ClearState()

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
	var got []*irc.Message

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
	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("NICK s[E]CuRE")),
		":robustirc.net 433 * s[E]CuRE :Nickname is already in use.")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("NICK S[E]CURE")),
		":robustirc.net 433 * S[E]CURE :Nickname is already in use.")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("NICK S{E}CURE")),
		":robustirc.net 433 * S{E}CURE :Nickname is already in use.")
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

	mustMatchMsg(t,
		ProcessMessage(id, irc.ParseMessage("NICK 0secure")),
		":robustirc.net 432 * 0secure :Erroneus nickname.")

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

	mustMatchIrcmsgs(t,
		ProcessMessage(id, irc.ParseMessage("JOIN #foobar")),
		[]*irc.Message{
			&irc.Message{Prefix: &s.ircPrefix, Command: irc.JOIN, Trailing: "#foobar"},
			irc.ParseMessage(":robustirc.net 331 secure #foobar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 secure = #foobar :@secure"),
			irc.ParseMessage(":robustirc.net 366 secure #foobar :End of /NAMES list."),
		})

	mustMatchMsg(t,
		ProcessMessage(id, irc.ParseMessage("JOIN foobar")),
		":robustirc.net 403 secure foobar :No such channel")
}

func TestInvalidPrivmsg(t *testing.T) {
	id := types.RobustId{Id: time.Now().UnixNano()}

	ClearState()
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}
	CreateSession(id, "authbytes")

	ProcessMessage(id, irc.ParseMessage("NICK secure"))
	ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	ProcessMessage(id, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		ProcessMessage(id, irc.ParseMessage("PRIVMSG #test")),
		":robustirc.net 412 secure :No text to send")

	mustMatchMsg(t,
		ProcessMessage(id, irc.ParseMessage("PRIVMSG #test foo")),
		":robustirc.net 412 secure :No text to send")

	mustMatchMsg(t,
		ProcessMessage(id, irc.ParseMessage("PRIVMSG")),
		":robustirc.net 411 secure :No recipient given (PRIVMSG)")
}

func TestKill(t *testing.T) {
	var got []*irc.Message

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
	mustMatchMsg(t, []*irc.Message{got[0]}, ":robustirc.net 001 mero :Welcome to RobustIRC!")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("KILL secure")),
		":robustirc.net 461 mero KILL :Not enough parameters")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("KILL")),
		":robustirc.net 461 mero KILL :Not enough parameters")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("KILL secure :die")),
		":robustirc.net 481 mero :Permission Denied - You're not an IRC operator")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("OPER mero bar")),
		":robustirc.net 464 mero :Password incorrect")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("OPER mero foo")),
		":robustirc.net 381 mero :You are now an IRC operator")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("KILL socoro :die")),
		":robustirc.net 401 mero socoro :No such nick/channel")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("KILL s[E]CuRE :die now, will you?")),
		":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: die now, will you?")

	if _, err := GetSession(idSecure); err == nil {
		t.Fatalf("GetSession(%v) returned a session after KILL", idSecure)
	}
}

func TestAway(t *testing.T) {
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

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("PRIVMSG mero :hey")),
		":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :hey")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("AWAY :upgrading server")),
		":robustirc.net 306 mero :You have been marked as being away")

	mustMatchIrcmsgs(t,
		ProcessMessage(idSecure, irc.ParseMessage("PRIVMSG mero :you there?")),
		[]*irc.Message{
			irc.ParseMessage(":s[E]CuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there?"),
			irc.ParseMessage(":robustirc.net 301 s[E]CuRE mero :upgrading server"),
		})

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("AWAY")),
		":robustirc.net 305 mero :You are no longer marked as being away")
}

// TestTopic tests that setting/getting topics works properly. Using topic as
// the channel’s state, it also verifies that joining/parting will
// create/delete channels.
func TestTopic(t *testing.T) {
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

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #nonexistant")),
		":robustirc.net 403 sECuRE #nonexistant :No such channel")

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test")),
		":robustirc.net 331 sECuRE #test :No topic is set")

	mustMatchIrcmsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :yeah, this is a topic.")),
		&irc.Message{
			Prefix:   &sSecure.ircPrefix,
			Command:  irc.TOPIC,
			Params:   []string{"#test"},
			Trailing: "yeah, this is a topic.",
		})

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :")

	ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :yeah, this is a topic."))

	mustMatchIrcmsgs(t,
		ProcessMessage(idMero, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			// TODO: the following line is flaky. We should set a defined time
			// on the server for testing instead of the server using time.Now().
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE " + strconv.FormatInt(time.Now().Unix(), 10)),
			irc.ParseMessage(":robustirc.net 353 mero = #test :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #test :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		ProcessMessage(idMero, irc.ParseMessage("TOPIC #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			// TODO: the following line is flaky. We should set a defined time
			// on the server for testing instead of the server using time.Now().
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE " + strconv.FormatInt(time.Now().Unix(), 10)),
		})

	mustMatchIrcmsg(t,
		ProcessMessage(idMero, irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchIrcmsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("TOPIC #test")),
		":robustirc.net 403 mero #test :No such channel")
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

func TestChannelMode(t *testing.T) {
	var got, want []*irc.Message

	ClearState()
	NetworkPassword = "foo"
	ServerPrefix = &irc.Prefix{Name: "robustirc.net"}

	idSecure := types.RobustId{Id: 1420228218166687917}
	idMero := types.RobustId{Id: 1420228218166687918}

	CreateSession(idSecure, "auth-secure")
	CreateSession(idMero, "auth-mero")

	ProcessMessage(idSecure, irc.ParseMessage("NICK sECuRE"))
	ProcessMessage(idSecure, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	ProcessMessage(idMero, irc.ParseMessage("NICK mero"))
	ProcessMessage(idMero, irc.ParseMessage("USER foo 0 * :Axel Wagner"))

	ProcessMessage(idMero, irc.ParseMessage("JOIN #test"))
	ProcessMessage(idSecure, irc.ParseMessage("JOIN #test"))

	// Set a topic from the outside.
	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :foobar")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :foobar")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("MODE #test +t")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +t")

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("MODE #test")),
		":robustirc.net 324 sECuRE #test +t")

	mustMatchMsg(t,
		ProcessMessage(idSecure, irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		ProcessMessage(idMero, irc.ParseMessage("TOPIC #test :bleh")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae TOPIC #test :bleh")
}
