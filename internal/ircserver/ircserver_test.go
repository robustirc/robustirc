package ircserver

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/config"
	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func stdIRCServer() (*IRCServer, map[string]robust.Id) {
	i := NewIRCServer("robustirc.net", time.Unix(0, 1481144012969203276))
	i.Config = config.Network{
		IRC: config.IRC{
			Operators: []config.IRCOp{
				{Name: "mero", Password: "foo"},
				{Name: "xeen", Password: "foo"},
			},
		},
	}

	ids := make(map[string]robust.Id)

	ids["secure"] = robust.Id{Id: 1420228218166687917}
	ids["mero"] = robust.Id{Id: 1420228218166687918}
	ids["xeen"] = robust.Id{Id: 1420228218166687919}

	i.CreateSession(ids["secure"], "auth-secure")
	i.CreateSession(ids["mero"], "auth-mero")
	i.CreateSession(ids["xeen"], "auth-xeen")

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("NICK sECuRE"))
	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("NICK mero"))
	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("USER foo 0 * :Axel Wagner"))
	i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("NICK xeen"))
	i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("USER baz 0 * :Iks Enn"))

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
func mustMatchIrcmsgs(t *testing.T, got *Replyctx, want []*irc.Message) {
	// TODO: mark mustMatchIrcmsgs as a helper function once
	// https://github.com/golang/go/issues/4899 is addressed.
	failed := len(got.Messages) != len(want)
	for idx := 0; !failed && idx < len(want); idx++ {
		failed = got.Messages[idx].Data != want[idx].String()
	}
	if failed {
		t.Logf("got (%d messages):\n", len(got.Messages))
		for _, msg := range got.Messages {
			t.Logf("    %s\n", msg.Data)
		}
		t.Logf("want (%d messages):\n", len(want))
		for _, msg := range want {
			t.Logf("    %s\n", msg.Bytes())
		}
		t.Fatalf("ProcessMessage() return value does not match expectation: got %v, want %v", got, want)
	}
}

func mustMatchIrcmsg(t *testing.T, got *Replyctx, want *irc.Message) {
	mustMatchIrcmsgs(t, got, []*irc.Message{want})
}

func mustMatchMsg(t *testing.T, got *Replyctx, want string) {
	mustMatchIrcmsgs(t, got, []*irc.Message{irc.ParseMessage(want)})
}

func TestSessionInitialization(t *testing.T) {
	i := NewIRCServer("robustirc.net", time.Now())
	i.Config = config.Network{}

	id := robust.Id{Id: time.Now().UnixNano()}
	i.CreateSession(id, "authbytes")

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("JOIN #test")),
		":robustirc.net 451 JOIN :You have not registered")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("NICK")),
		":robustirc.net 431 :No nickname given")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("NICK secure")),
		[]*irc.Message{})
	got := i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	if len(got.Messages) < 1 || irc.ParseMessage(got.Messages[0].Data).Command != irc.RPL_WELCOME {
		t.Fatalf("got %v, want irc.RPL_WELCOME", got)
	}

	if s.Nick != "secure" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}

	if !s.loggedIn {
		t.Fatalf("session.loggedIn() still false after sending NICK and USER")
	}

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("JOINT #test")),
		":robustirc.net 421 secure JOINT :Unknown command")

	// Now connect again with the same nickname and verify the server behaves
	// correctly in that scenario.

	idSecond := robust.Id{Id: time.Now().UnixNano()}
	i.CreateSession(idSecond, "authbytes")

	sSecond, err := i.GetSession(idSecond)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if sSecond.loggedIn {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, idSecond, irc.ParseMessage("NICK :secure")),
		":robustirc.net 433 * secure :Nickname is already in use")
	if sSecond.Nick != "" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "")
	}
	i.ProcessMessage(robust.Id{}, idSecond, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))

	got = i.ProcessMessage(robust.Id{}, idSecond, irc.ParseMessage("NICK secure_"))
	if len(got.Messages) < 1 || irc.ParseMessage(got.Messages[0].Data).Command != irc.RPL_WELCOME {
		t.Fatalf("got %v, want irc.RPL_WELCOME", got)
	}

	if sSecond.Nick != "secure_" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure")
	}

	if !sSecond.loggedIn {
		t.Fatalf("session.loggedIn() still false after sending NICK and USER")
	}
}

func welcomeMustContain(t *testing.T, passMsg, privMsg string) {
	i := NewIRCServer("robustirc.net", time.Now())
	i.Config = config.Network{
		IRC: config.IRC{
			Operators: []config.IRCOp{
				{Name: "mero", Password: "foo"},
			},
		},
	}

	id := robust.Id{Id: 0x13c988ab2b01f2fb}
	i.CreateSession(id, "authbytes")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage(passMsg)),
		[]*irc.Message{})
	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("NICK secure")),
		[]*irc.Message{})
	got := i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	if len(got.Messages) < 1 || irc.ParseMessage(got.Messages[0].Data).Command != irc.RPL_WELCOME {
		t.Fatalf("got %v, want irc.RPL_WELCOME", got)
	}
	foundAuth := false
	for _, msg := range got.Messages {
		if msg.Data == privMsg {
			foundAuth = true
			break
		}
	}
	if !foundAuth {
		t.Fatalf("No PRIVMSG to NickServ in %v", got)
	}
}

func TestNickServAuth(t *testing.T) {
	welcomeMustContain(t,
		"PASS :foobar",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY foobar")

	welcomeMustContain(t,
		"PASS :nickserv=foobar",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY foobar")

	welcomeMustContain(t,
		"PASS :nickserv=foobar=baz",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY foobar=baz")

	welcomeMustContain(t,
		"PASS :nickserv=pass:word",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY pass:word")

	welcomeMustContain(t,
		"PASS password",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY password")

	welcomeMustContain(t,
		"PASS :nickserv=secure foobar",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY secure foobar")

	welcomeMustContain(t,
		"PASS :nickserv=foobar:oper=blah",
		":secure!blah@robust/0x13c988ab2b01f2fb PRIVMSG NickServ :IDENTIFY foobar")

	welcomeMustContain(t,
		"PASS :nickserv=foobar:oper=mero foo",
		":robustirc.net 381 secure :You are now an IRC operator")
}

func mustMatchInterestedMsgs(t *testing.T, i *IRCServer, msg *irc.Message, msgs []*robust.Message, sessions []robust.Id, want []bool) {
	if len(want) != len(sessions) {
		panic("bug: len(want) != len(sessions)")
	}

	failed := false
	got := make([]bool, len(want))
	for idx, sessionid := range sessions {
		// TODO(secure): We might need to refactor this to do a finer-grained
		// comparison instead of just the first message.
		if len(msgs) == 0 {
			failed = true
			break
		}
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

func robustMessagesFromReply(replies *Replyctx) []*robust.Message {
	converted := make([]*robust.Message, len(replies.Messages))
	for idx, msg := range replies.Messages {
		converted[idx] = &robust.Message{
			Id:             msg.Id,
			Type:           robust.IRCToClient,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		}
	}
	return converted
}

func mustMatchInterested(t *testing.T, i *IRCServer, sessionid robust.Id, msg *irc.Message, sessions []robust.Id, want []bool) {
	msgid := robust.Id{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(msgid, sessionid, msg)
	mustMatchInterestedMsgs(t, i, msg, robustMessagesFromReply(replies), sessions, want)
}

func TestInterestedIn(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("NICK secure_out_of_chan"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("NICK secore"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("TOPIC #test :foobar"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PRIVMSG #test :foobar"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("MODE #test +o mero"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("MODE secore +i"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PRIVMSG xeen :foobar"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("PART #test"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["mero"], irc.ParseMessage("KICK #test secore :bye"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})

	mustMatchInterested(t, i,
		ids["secure"], irc.ParseMessage("QUIT :bye"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, false})

	i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchInterested(t, i,
		ids["mero"], irc.ParseMessage("QUIT :bye"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})
}

func TestInterestedInDelayed(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	msg := irc.ParseMessage("NICK secore")
	msgid := robust.Id{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(msgid, ids["secure"], msg)

	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test"))

	mustMatchInterestedMsgs(t, i,
		msg, robustMessagesFromReply(replies),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})
}

func TestServiceAliases(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	aliases := map[string]string{
		"NickServ": "PRIVMSG NickServ :",
		"ns":       "PRIVMSG NickServ :",
		"ChanServ": "PRIVMSG ChanServ :",
		"cs":       "PRIVMSG ChanServ :",
		"OperServ": "PRIVMSG OperServ :",
		"os":       "PRIVMSG OperServ :",
		"MemoServ": "PRIVMSG MemoServ :",
		"ms":       "PRIVMSG MemoServ :",
		"HostServ": "PRIVMSG HostServ :",
		"hs":       "PRIVMSG HostServ :",
		"BotServ":  "PRIVMSG BotServ :",
		"bs":       "PRIVMSG BotServ :",
	}

	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK NickServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))
	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))
	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK OperServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))
	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK MemoServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))
	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK HostServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))
	i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage("NICK BotServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server"))

	for alias, expanded := range aliases {
		mustMatchMsg(t,
			i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage(alias+" IDENTIFY foobar baz")),
			":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad "+expanded+"IDENTIFY foobar baz")
	}
}

func TestCaptchaLogin(t *testing.T) {
	i, _ := stdIRCServer()

	i.Config.CaptchaURL = "http://localhost"
	hmacSecret, _ := hex.DecodeString("6c6bb74d790942c92bde6b07e223e59d0f0aa75394625a0f98a69095296c7d85")
	i.Config.CaptchaHMACSecret = hmacSecret
	i.Config.CaptchaRequiredForLogin = true

	id := robust.Id{Id: 1420228218166687919}
	i.CreateSession(id, "authbytes")

	s, err := i.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%v) did not return a session", id)
	}

	if s.loggedIn {
		t.Fatalf("session.loggedIn() true before sending NICK")
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("NICK attacker")),
		[]*irc.Message{})

	captchaRequiredMsgs := []*irc.Message{
		irc.ParseMessage(":robustirc.net NOTICE attacker :To login, please go to http://localhost/#bG9naW46MTQyMDIyODIxODE2NjY4NzkxOTo=.YXV0aGJ5dGU=.tB+/UIcedHdT4YZwRaDOwu4wBJ9e0RMqo0RrCg/pc6s="),
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("USER attacker a a :a")),
		captchaRequiredMsgs)

	// Invalid password
	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("PASS :foo")),
		captchaRequiredMsgs)

	// Invalid captcha password
	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("PASS :captcha=foo")),
		captchaRequiredMsgs)

	u, err := url.Parse(i.generateCaptchaURL(s, fmt.Sprintf("okay:login:%d:", id.Id+1)))
	if err != nil {
		t.Fatal(err)
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, id, irc.ParseMessage("PASS :captcha="+u.Fragment)),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 001 attacker :Welcome to RobustIRC!"),
			irc.ParseMessage(":robustirc.net 002 attacker :Your host is robustirc.net"),
			irc.ParseMessage(":robustirc.net 003 attacker :This server was created 2016-12-07 20:53:32.969203276 +0000 UTC"),
			irc.ParseMessage(":robustirc.net 004 attacker :robustirc.net v1 i nstix"),
			irc.ParseMessage(":robustirc.net 005 CHANTYPES=# CHANNELLEN=32 NICKLEN=30 MODES=1 PREFIX=(o)@ KNOCK :are supported by this server"),
			irc.ParseMessage("NICK attacker 1 1 attacker robust/0x13b5aa0a2bcfb8af robustirc.net 0 + :a"),
			irc.ParseMessage(":robustirc.net 375 attacker :- robustirc.net Message of the day -"),
			irc.ParseMessage(":robustirc.net 372 attacker :- No MOTD configured yet."),
			irc.ParseMessage(":robustirc.net 376 attacker :End of MOTD command"),
		})
}

func TestSessionLimit(t *testing.T) {
	i := NewIRCServer("robustirc.net", time.Now())
	i.Config = config.Network{
		MaxSessions: 1,
	}

	if err := i.CreateSession(robust.Id{}, "authbytes"); err != nil {
		t.Fatalf("Could not create session: %v", err)
	}

	if err := i.CreateSession(robust.Id{}, "authbytes"); err == nil {
		t.Fatal("Unexpectedly could create second session")
	}
}

func TestChannelLimit(t *testing.T) {
	i, ids := stdIRCServer()
	i.Config = config.Network{
		MaxChannels: 1,
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#test"),
			irc.ParseMessage(":robustirc.net MODE #test +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :@xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #test +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #test :@xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #test :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("JOIN #second")),
		":robustirc.net 403 xeen #second :No such channel")
}
