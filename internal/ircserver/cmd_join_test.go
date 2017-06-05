package ircserver

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestInvalidChannelPlumbing(t *testing.T) {
	i, ids := stdIRCServer()
	s, _ := i.GetSession(ids["secure"])

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #foobar")),
		[]*irc.Message{
			{Prefix: &s.ircPrefix, Command: irc.JOIN, Params: []string{"#foobar"}},
			irc.ParseMessage(":robustirc.net MODE #foobar +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #foobar :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #foobar +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #foobar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #foobar :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #foobar :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN foobar")),
		":robustirc.net 403 sECuRE foobar :No such channel")
}

func TestJoinMultiple(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test,#second")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#test"),
			irc.ParseMessage(":robustirc.net MODE #test +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #test +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#second"),
			irc.ParseMessage(":robustirc.net MODE #second +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #second :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #second +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #second :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #second :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #second :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test,#second")),
		[]*irc.Message{})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #third,invalid,#fourth")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#third"),
			irc.ParseMessage(":robustirc.net MODE #third +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #third :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #third +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #third :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #third :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #third :End of /NAMES list."),
			irc.ParseMessage(":robustirc.net 403 sECuRE invalid :No such channel"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#fourth"),
			irc.ParseMessage(":robustirc.net MODE #fourth +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #fourth :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #fourth +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #fourth :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #fourth :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #fourth :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PART #second,#fourth")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PART #second"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PART #fourth"),
		})
}

func TestCaptchaJoin(t *testing.T) {
	i, ids := stdIRCServer()

	sMero, _ := i.GetSession(ids["mero"])
	meroCreated := time.Unix(0, sMero.Created)

	i.UpdateLastClientMessageID(&robust.Message{
		Session:  ids["mero"],
		Id:       robust.Id{},
		UnixNano: meroCreated.Add(1 * time.Minute).UnixNano(),
		Type:     robust.IRCFromClient,
	})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +x")),
		":robustirc.net NOTICE sECuRE :Cannot set mode +x, no CaptchaURL/CaptchaHMACSecret configured")

	i.Config.CaptchaURL = "http://localhost"
	hmacSecret, _ := hex.DecodeString("6c6bb74d790942c92bde6b07e223e59d0f0aa75394625a0f98a69095296c7d85")
	i.Config.CaptchaHMACSecret = hmacSecret

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +x")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +x")

	for _, message := range []string{
		// Verify joining the channel without a key does not work.
		"JOIN #test",
		// Verify invalid keys don’t get us into the channel.
		"JOIN #test invalid",
		// Verify the captcha challenge itself doesn’t get us into the channel.
		"JOIN #test am9pbjoxNDIwMjI4Mjc4MTY2Njg3OTE4OiN0ZXN0.YXV0aC1tZXI=.MQi3m1acLjBq9Kcgr59BkLOs7zfUw+co0XYVJh0MKtY=",
	} {
		mustMatchIrcmsgs(t,
			i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage(message)),
			[]*irc.Message{
				irc.ParseMessage(":robustirc.net NOTICE mero :To join #test, please go to http://localhost/#am9pbjoxNDIwMjI4Mjc4MTY2Njg3OTE4OiN0ZXN0.YXV0aC1tZXI=.MQi3m1acLjBq9Kcgr59BkLOs7zfUw+co0XYVJh0MKtY="),
				irc.ParseMessage(":robustirc.net 473 mero #test :Cannot join channel (+x). Please go to http://localhost/#am9pbjoxNDIwMjI4Mjc4MTY2Njg3OTE4OiN0ZXN0.YXV0aC1tZXI=.MQi3m1acLjBq9Kcgr59BkLOs7zfUw+co0XYVJh0MKtY="),
			})
	}

	// Verify a solved captcha gets us into the channel.
	s, _ := i.GetSession(ids["mero"])
	u, err := url.Parse(i.generateCaptchaURL(s, fmt.Sprintf("okay:join:%d:#test", ids["mero"].Id+1)))
	if err != nil {
		t.Fatal(err)
	}
	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test "+u.Fragment)),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae JOIN :#test"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #test +ntx"),
			irc.ParseMessage(":robustirc.net 331 mero #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #test :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #test :End of /NAMES list."),
		})

	// Verify a solved captcha also gets us into other channels, for 1 minute.
	for _, name := range []string{"#second", "#third"} {
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN "+name))
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage(fmt.Sprintf("MODE %s +x", name)))
	}

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #second")),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae JOIN :#second"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #second :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #second +ntx"),
			irc.ParseMessage(":robustirc.net 331 mero #second :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #second :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #second :End of /NAMES list."),
		})

	i.UpdateLastClientMessageID(&robust.Message{
		Session:  ids["mero"],
		Id:       robust.Id{},
		UnixNano: meroCreated.Add(2 * time.Minute).UnixNano(),
		Type:     robust.IRCFromClient,
	})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #third")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net NOTICE mero :To join #third, please go to http://localhost/#am9pbjoxNDIwMjI4MzM4MTY2Njg3OTE4OiN0aGlyZA==.YXV0aC1tZXI=.P4k2eEfS52KzS/DDZwDrwNEUvLt0hLZQffBymuTUDVA="),
			irc.ParseMessage(":robustirc.net 473 mero #third :Cannot join channel (+x). Please go to http://localhost/#am9pbjoxNDIwMjI4MzM4MTY2Njg3OTE4OiN0aGlyZA==.YXV0aC1tZXI=.P4k2eEfS52KzS/DDZwDrwNEUvLt0hLZQffBymuTUDVA="),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +o mero"))

	// Verify invites override captchas
	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("INVITE xeen #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 mero xeen #test"),
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae INVITE xeen :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :mero invited xeen into the channel."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#test"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #test +ntx"),
			irc.ParseMessage(":robustirc.net 331 xeen #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #test :@mero @sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #test :End of /NAMES list."),
		})
}

func TestChannelCaseInsensitive(t *testing.T) {
	i, ids := stdIRCServer()

	sMero, _ := i.GetSession(ids["mero"])

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Params: []string{"#TEST"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 mero #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #TEST :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #TEST :End of /NAMES list."),
		})
}

func TestBanned(t *testing.T) {
	i, ids := stdIRCServer()
	sMero, _ := i.GetSession(ids["mero"])

	i.ProcessMessage(&robust.Message{Session: ids["mero"], RemoteAddr: "127.0.0.1"}, irc.ParseMessage("PING"))
	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b *@127.0.0.1")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +b *@127.0.0.1")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 474 mero #test :Cannot join channel (+b)"),
		})

	// mero roams to a non-banned IP address.
	i.ProcessMessage(&robust.Message{Session: ids["mero"], RemoteAddr: "127.0.0.2"}, irc.ParseMessage("PING"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Params: []string{"#TEST"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 mero #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #TEST :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #TEST :End of /NAMES list."),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"], RemoteAddr: "127.0.0.2"}, irc.ParseMessage("PART #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b *@127.*")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +b *@127.*")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 367 sECuRE #test *@127.*"),
			irc.ParseMessage(":robustirc.net 367 sECuRE #test *@127.0.0.1"),
			irc.ParseMessage(":robustirc.net 368 sECuRE #test :End of Channel Ban List"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 474 mero #test :Cannot join channel (+b)"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test -b *@127.*")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test -b *@127.*")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 367 sECuRE #test *@127.0.0.1"),
			irc.ParseMessage(":robustirc.net 368 sECuRE #test :End of Channel Ban List"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Params: []string{"#TEST"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 mero #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #TEST :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #TEST :End of /NAMES list."),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("PART #test"))

	// ban a robustid
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b *!*foo@robust/0x13b5aa0a2bcfb8ae")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +b *!*foo@robust/0x13b5aa0a2bcfb8ae")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 367 sECuRE #test *!*foo@robust/0x13b5aa0a2bcfb8ae"),
			irc.ParseMessage(":robustirc.net 367 sECuRE #test *@127.0.0.1"),
			irc.ParseMessage(":robustirc.net 368 sECuRE #test :End of Channel Ban List"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 474 mero #test :Cannot join channel (+b)"),
		})

	// mero roams to a non-banned IP address.
	i.ProcessMessage(&robust.Message{Session: ids["mero"], RemoteAddr: "192.168.1.2"}, irc.ParseMessage("PING"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 474 mero #test :Cannot join channel (+b)"),
		})
}
