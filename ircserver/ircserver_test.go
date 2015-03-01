package ircserver

import (
	"bytes"
	"testing"
	"time"

	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

func stdIRCServer() (*IRCServer, map[string]types.RobustId) {
	i := NewIRCServer("robustirc.net", time.Now())
	i.Config = config.IRC{
		Operators: []config.IRCOp{
			config.IRCOp{Name: "mero", Password: "foo"},
			config.IRCOp{Name: "xeen", Password: "foo"},
		},
	}

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

	mustMatchMsg(t,
		i.ProcessMessage(id, irc.ParseMessage("NICK")),
		":robustirc.net 431 :No nickname given")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(id, irc.ParseMessage("NICK secure")),
		[]*irc.Message{})
	got := i.ProcessMessage(id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	if len(got) < 1 || got[0].Command != irc.RPL_WELCOME {
		t.Fatalf("got %v, want irc.RPL_WELCOME", got)
	}

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
		":robustirc.net 433 * secure :Nickname is already in use")
	if sSecond.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
	i.ProcessMessage(idSecond, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	got = i.ProcessMessage(idSecond, irc.ParseMessage("NICK secure_"))
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

// TestPlumbing exercises the code paths for storing messages in outputstream
// and getting them from multiple sessions.
func TestPlumbing(t *testing.T) {
	i, ids := stdIRCServer()

	msgid := types.RobustId{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.SendMessages(replies, ids["secure"], msgid.Id)
	got, ok := i.Get(msgid)
	if !ok {
		t.Fatalf("_, ok := Get(%d); got false, want true", msgid.Id)
	}
	if len(got) != len(replies) {
		t.Fatalf("len(got): got %d, want %d", len(got), len(replies))
	}
	if got[0].Data != string(replies[0].Bytes()) {
		t.Fatalf("message 0: got %v, want %v", got[0].Data, string(replies[0].Bytes()))
	}

	nextid := types.RobustId{Id: time.Now().UnixNano()}
	replies = i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #foobar"))
	i.SendMessages(replies, ids["secure"], nextid.Id)
	got = i.GetNext(msgid)
	if !ok {
		t.Fatalf("_, ok := Get(%d); got false, want true", msgid.Id)
	}
	if len(got) != len(replies) {
		t.Fatalf("len(got): got %d, want %d", len(got), len(replies))
	}
	if got[0].Data != string(replies[0].Bytes()) {
		t.Fatalf("message 0: got %v, want %v", got[0].Data, string(replies[0].Bytes()))
	}

	if got[0].InterestingFor[ids["mero"].Id] {
		t.Fatalf("sMero interestedIn JOIN to #foobar, expected false")
	}

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #baz"))

	msgid = types.RobustId{Id: time.Now().UnixNano()}
	replies = i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #baz"))
	i.SendMessages(replies, ids["secure"], msgid.Id)
	got, _ = i.Get(msgid)
	if !got[0].InterestingFor[ids["mero"].Id] {
		t.Fatalf("sMero not interestedIn JOIN to #baz, expected true")
	}
}

func mustMatchInterestedMsgs(t *testing.T, i *IRCServer, msg *irc.Message, msgs []*types.RobustMessage, sessions []types.RobustId, want []bool) {
	if len(want) != len(sessions) {
		panic("bug: len(want) != len(sessions)")
	}

	failed := false
	got := make([]bool, len(want))
	for idx, sessionid := range sessions {
		// TODO(secure): We might need to refactor this to do a finer-grained
		// comparison instead of just the first message.
		got[idx] = msgs[0].InterestingFor[sessionid.Id]
		if got[idx] != want[idx] {
			failed = true
		}
	}

	if failed {
		t.Logf("mismatch for input %q, output %v:\n", msg, msgs)
		for idx, sessionid := range sessions {
			s, err := i.GetSession(sessionid)
			nick := "<quitted>"
			if err == nil {
				nick = s.Nick
			}
			t.Logf("  %s got = %v, want = %v\n", nick, got[idx], want[idx])
		}
		t.Fatalf("InterestedIn() mismatch")
	}
}

func mustMatchInterested(t *testing.T, i *IRCServer, sessionid types.RobustId, msg *irc.Message, sessions []types.RobustId, want []bool) {
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(sessionid, msg)
	i.SendMessages(replies, sessionid, msgid.Id)
	msgs, _ := i.Get(msgid)
	mustMatchInterestedMsgs(t, i, msg, msgs, sessions, want)
}

func TestInterestedIn(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("NICK secure_out_of_chan"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("NICK secore"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("TOPIC #test :foobar"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PRIVMSG #test :foobar"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("MODE #test +o mero"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("MODE secore +i"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PRIVMSG xeen :foobar"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PART #test"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["mero"], irc.ParseMessage("KICK #test secore :bye"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("QUIT :bye"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["mero"], irc.ParseMessage("QUIT :bye"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, true, true})
}

func TestInterestedInDelayed(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	msg := irc.ParseMessage("NICK secore")
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(ids["secure"], msg)
	i.SendMessages(replies, ids["secure"], msgid.Id)
	msgs, _ := i.Get(msgid)

	i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test"))

	mustMatchInterestedMsgs(t, i,
		msg, msgs,
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})
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
		":robustirc.net 433 * s[E]CuRE :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(idMero, irc.ParseMessage("NICK S[E]CURE")),
		":robustirc.net 433 * S[E]CURE :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(idMero, irc.ParseMessage("NICK S{E}CURE")),
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

	if s.loggedIn() {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(id, irc.ParseMessage("NICK 0secure")),
		":robustirc.net 432 * 0secure :Erroneous nickname")

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
			irc.ParseMessage(":robustirc.net SJOIN 1 #foobar :@sECuRE"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #foobar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #foobar :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #foobar :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN foobar")),
		":robustirc.net 403 sECuRE foobar :No such channel")
}

func TestPing(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PING")),
		":robustirc.net 409 sECuRE :No origin specified")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PING foobar")),
		":robustirc.net PONG foobar")
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

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PRIVMSG sorcix :foo")),
		":robustirc.net 401 sECuRE sorcix :No such nick/channel")
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

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("OPER mero foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 mero :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE mero :+o"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL socoro :die")),
		":robustirc.net 401 mero socoro :No such nick/channel")

	replies := i.ProcessMessage(ids["mero"], irc.ParseMessage("KILL sECuRE :die now, will you?"))
	mustMatchMsg(t,
		replies,
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: die now, will you?")
	// SendMessages will actually delete the session, as it may still be
	// required within SendMessages to determine where a message should be sent
	// to (the QUIT message itself, notably).
	i.SendMessages(replies, ids["secure"], time.Now().UnixNano())

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
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 331 sECuRE #test :No topic is set")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic.")),
		[]*irc.Message{
			&irc.Message{
				Prefix:   &sSecure.ircPrefix,
				Command:  irc.TOPIC,
				Params:   []string{"#test"},
				Trailing: "yeah, this is a topic.",
			},
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :yeah, this is a topic."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 0 :"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic."))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :mero"),
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

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	mustMatchIrcmsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PART #test")),
		":robustirc.net 403 sECuRE #test :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 403 mero #test :No such channel")

	// Same with QUIT
	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	i.ProcessMessage(ids["secure"], irc.ParseMessage("QUIT"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 403 mero #test :No such channel")
}

func TestMotd(t *testing.T) {
	var got []*irc.Message

	i, _ := stdIRCServer()

	idSecure := types.RobustId{Id: 1420228218166687917}

	i.CreateSession(idSecure, "auth-secure")

	i.ProcessMessage(idSecure, irc.ParseMessage("USER 1 2 3 :4"))
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
	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :foobar")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :foobar"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :foobar"),
		})

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

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("TOPIC #test :bleh")),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae TOPIC #test :bleh"),
			irc.ParseMessage(":mero TOPIC #test mero 1420228218 :bleh"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +o sECuRE")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +o sECuRE")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +o nobody")),
		":robustirc.net 441 mero nobody #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +x sECuRE")),
		":robustirc.net 472 mero x :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #nonexistant +x sECuRE")),
		":robustirc.net 442 mero #nonexistant :You're not on that channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :finally")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :finally"),
		})
}

func TestUserMode(t *testing.T) {
	i, ids := stdIRCServer()

	// User modes are not yet implemented.
	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("MODE sECuRE +i")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+")
}

func TestBans(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	// Bans are not yet implemented.
	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("MODE #test b")),
		":robustirc.net 368 sECuRE #test :End of Channel Ban List")
}

func TestChannelMemberStatus(t *testing.T) {
	i, ids := stdIRCServer()

	sSecure, _ := i.GetSession(ids["secure"])

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.JOIN, Trailing: "#test"},
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :sECuRE"),
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

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :finally")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :finally"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("TOPIC #test :nooo")),
		":robustirc.net 482 xeen #test :You're not channel operator")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("OPER xeen foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 xeen :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE xeen :+o"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("MODE #test +o xeen")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE #test +o xeen")
}

func TestWho(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHO")),
		":robustirc.net 315 sECuRE :End of /WHO list")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHO #nonexistant")),
		":robustirc.net 315 sECuRE #nonexistant :End of /WHO list")

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

func TestKick(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	sXeen, _ := i.GetSession(ids["xeen"])

	mustMatchMsg(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("KICK #test secure :bye")),
		":robustirc.net 482 mero #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("KICK #test secure :bye")),
		":robustirc.net 442 xeen #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("KICK #toast secure :bye")),
		":robustirc.net 403 xeen #toast :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("KICK #test moro :bye bye")),
		":robustirc.net 441 sECuRE moro #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("KICK #test mero :bye bye")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad KICK #test mero :bye bye")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			&irc.Message{Prefix: &sXeen.ircPrefix, Command: irc.JOIN, Trailing: "#TEST"},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})
}

func TestChannelCaseInsensitive(t *testing.T) {
	i, ids := stdIRCServer()

	sMero, _ := i.GetSession(ids["mero"])

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Trailing: "#TEST"},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :mero"),
			irc.ParseMessage(":robustirc.net 331 mero #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #TEST :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #TEST :End of /NAMES list."),
		})
}

func TestWhois(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS bero")),
		":robustirc.net 401 sECuRE bero :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("OPER mero foo"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("AWAY :cleaning dishes"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #second"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #second"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #test +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("PART #test"))
	i.ProcessMessage(ids["secure"], irc.ParseMessage("OPER mero foo"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})
}

func TestList(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		":robustirc.net 323 sECuRE :End of LIST")

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 1 :"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("TOPIC #test :this is a topic"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 1 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #new"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #new 1 :"),
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST #test,#new")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 322 sECuRE #new 1 :"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST invalid,#test,invalid")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(ids["mero"], irc.ParseMessage("MODE #new +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})
}

func TestJoinMultiple(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test,#second")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#test"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#second"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #second :@sECuRE"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #second :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #second :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #second :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test,#second")),
		[]*irc.Message{})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #third,invalid,#fourth")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#third"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #third :@sECuRE"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #third :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #third :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #third :End of /NAMES list."),
			irc.ParseMessage(":robustirc.net 403 sECuRE invalid :No such channel"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#fourth"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #fourth :@sECuRE"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #fourth :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #fourth :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #fourth :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("PART #second,#fourth")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PART #second"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PART #fourth"),
		})
}
