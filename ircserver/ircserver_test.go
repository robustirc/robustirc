package ircserver

import (
	"bytes"
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

func stdIRCServer() (*IRCServer, map[string]types.RobustId) {
	i := NewIRCServer("robustirc.net", time.Now())

	NetworkPassword = "foo"

	ids := make(map[string]types.RobustId)

	ids["secure"] = types.RobustId{Id: 1420228218166687917}
	ids["mero"] = types.RobustId{Id: 1420228218166687918}
	ids["xeen"] = types.RobustId{Id: 1420228218166687919}

	i.CreateSession(ids["secure"], "auth-secure")
	i.CreateSession(ids["mero"], "auth-mero")
	i.CreateSession(ids["xeen"], "auth-xeen")

	i.ProcessMessage(ids["secure"], irc.ParseMessage("NICK sECuRE"))
	i.ProcessMessage(ids["secure"], irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("NICK mero"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("USER foo 0 * :Axel Wagner"))
	i.ProcessMessage(ids["xeen"], irc.ParseMessage("NICK xeen"))
	i.ProcessMessage(ids["xeen"], irc.ParseMessage("USER baz 0 * :Iks Enn"))

	return i, ids
}

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
	i := NewIRCServer("robustirc.net", time.Now())

	id := types.RobustId{Id: time.Now().UnixNano()}
	i.CreateSession(id, "authbytes")

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(id, irc.ParseMessage("JOIN #test")),
		":robustirc.net 451 JOIN :You have not registered")

	i.ProcessMessage(id, irc.ParseMessage("NICK secure"))
	i.ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	if s.Nick != "secure" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}

	if !s.loggedIn() {
		t.Fatalf("session.loggedIn() still false after sending NICK and USER")
	}

	mustMatchMsg(t,
		i.ProcessMessage(id, irc.ParseMessage("JOINT #test")),
		":robustirc.net 421 secure JOINT :Unknown command")

	// Now connect again with the same nickname and verify the server behaves
	// correctly in that scenario.

	idSecond := types.RobustId{Id: time.Now().UnixNano()}
	i.CreateSession(idSecond, "authbytes")

	sSecond, err := i.GetSession(idSecond)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if sSecond.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(idSecond, irc.ParseMessage("NICK secure")),
		":robustirc.net 433 * secure :Nickname is already in use.")
	if sSecond.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
	i.ProcessMessage(idSecond, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	got := i.ProcessMessage(idSecond, irc.ParseMessage("NICK secure_"))
	if len(got) < 1 || got[0].Command != irc.RPL_WELCOME {
		t.Fatalf("got %v, want irc.RPL_WELCOME", got)
	}

	if sSecond.Nick != "secure_" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}

	if !sSecond.loggedIn() {
		t.Fatalf("session.loggedIn() still false after sending NICK and USER")
	}
}

func TestNickCollision(t *testing.T) {
	var got []*irc.Message

	i, _ := stdIRCServer()

	idSecure := types.RobustId{Id: 1420228218166687333}
	idMero := types.RobustId{Id: 1420228218166687444}

	i.CreateSession(idSecure, "auth-secure")
	i.CreateSession(idMero, "auth-mero")

	got = i.ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	if len(got) > 0 {
		for _, msg := range got {
			if msg.Command != irc.ERR_NICKNAMEINUSE {
				continue
			}
			t.Fatalf("got %v, wanted anything but ERR_NICKNAMEINUSE", msg)
		}
	}

	mustMatchMsg(t,
		i.ProcessMessage(idMero, irc.ParseMessage("NICK s[E]CuRE")),
		":robustirc.net 433 * s[E]CuRE :Nickname is already in use.")

	mustMatchMsg(t,
		i.ProcessMessage(idMero, irc.ParseMessage("NICK S[E]CURE")),
		":robustirc.net 433 * S[E]CURE :Nickname is already in use.")

	mustMatchMsg(t,
		i.ProcessMessage(idMero, irc.ParseMessage("NICK S{E}CURE")),
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

	i, _ := stdIRCServer()

	id := types.RobustId{Id: time.Now().UnixNano()}
	i.CreateSession(id, "authbytes")

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(id, irc.ParseMessage("NICK 0secure")),
		":robustirc.net 432 * 0secure :Erroneus nickname.")

	if s.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
}

func TestInvalidChannelPlumbing(t *testing.T) {
	i, ids := stdIRCServer()
	s, _ := i.GetSession(ids["secure"])

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #foobar")),
		[]*irc.Message{
			&irc.Message{Prefix: &s.ircPrefix, Command: irc.JOIN, Trailing: "#foobar"},
			irc.ParseMessage(":robustirc.net 331 sECuRE #foobar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #foobar :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #foobar :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN foobar")),
		":robustirc.net 403 sECuRE foobar :No such channel")
}

func TestInvalidPrivmsg(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG #test")),
		":robustirc.net 412 sECuRE :No text to send")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG #test foo")),
		":robustirc.net 412 sECuRE :No text to send")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG")),
		":robustirc.net 411 sECuRE :No recipient given (PRIVMSG)")
}

func TestKill(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL secure")),
		":robustirc.net 461 mero KILL :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL")),
		":robustirc.net 461 mero KILL :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL secure :die")),
		":robustirc.net 481 mero :Permission Denied - You're not an IRC operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("OPER mero bar")),
		":robustirc.net 464 mero :Password incorrect")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("OPER mero foo")),
		":robustirc.net 381 mero :You are now an IRC operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL socoro :die")),
		":robustirc.net 401 mero socoro :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL sECuRE :die now, will you?")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: die now, will you?")

	if _, err := i.GetSession(ids["secure"]); err == nil {
		t.Fatalf("GetSession(%v) returned a session after KILL", ids["secure"])
	}
}

func TestAway(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG mero :hey")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :hey")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("AWAY :upgrading server")),
		":robustirc.net 306 mero :You have been marked as being away")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG mero :you there?")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there?"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :upgrading server"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("AWAY")),
		":robustirc.net 305 mero :You are no longer marked as being away")
}

// TestTopic tests that setting/getting topics works properly. Using topic as
// the channel’s state, it also verifies that joining/parting will
// create/delete channels.
func TestTopic(t *testing.T) {
	i, ids := stdIRCServer()

	sSecure, _ := i.GetSession(ids["secure"])
	sMero, _ := i.GetSession(ids["mero"])

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #nonexistant")),
		":robustirc.net 403 sECuRE #nonexistant :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 331 sECuRE #test :No topic is set")

	mustMatchIrcmsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic.")),
		&irc.Message{
			Prefix:   &sSecure.ircPrefix,
			Command:  irc.TOPIC,
			Params:   []string{"#test"},
			Trailing: "yeah, this is a topic.",
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :")

	i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic."))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE 1420228218"),
			irc.ParseMessage(":robustirc.net 353 mero = #test :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #test :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE 1420228218"),
		})

	mustMatchIrcmsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchIrcmsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 403 mero #test :No such channel")
}

func TestMotd(t *testing.T) {
	var got []*irc.Message

	i, _ := stdIRCServer()

	idSecure := types.RobustId{Id: 1420228218166687917}

	i.CreateSession(idSecure, "auth-secure")

	got = i.ProcessMessage(idSecure, irc.ParseMessage("NICK s[E]CuRE"))
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
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	// Set a topic from the outside.
	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :foobar")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :foobar")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +t")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("MODE #test")),
		":robustirc.net 324 sECuRE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test :bleh")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae TOPIC #test :bleh")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +o sECuRE")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +o sECuRE")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :finally")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally")
}

func TestChannelMemberStatus(t *testing.T) {
	i, ids := stdIRCServer()

	sSecure, _ := i.GetSession(ids["secure"])

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
			irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@mero sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})

	i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +t")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +o xeen")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +o xeen")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("MODE #test -o+o xeen sECuRE")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE #test -o+o xeen sECuRE")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("MODE #test +o xeen")),
		":robustirc.net 482 xeen #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :finally")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("TOPIC #test :nooo")),
		":robustirc.net 482 xeen #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("OPER xeen foo")),
		":robustirc.net 381 xeen :You are now an IRC operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("MODE #test +o xeen")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE #test +o xeen")
}

func TestWho(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("MODE #test +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["secure"], irc.ParseMessage("AWAY :afk"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("NICK secore"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net secore G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["xeen"], irc.ParseMessage("join #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net secore G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})
}

func TestQuit(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("quit :bye bye"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["xeen"], irc.ParseMessage("part #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(ids["xeen"], irc.ParseMessage("join #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})
}
