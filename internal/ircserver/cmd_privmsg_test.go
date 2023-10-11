package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestInvalidPrivmsg(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG #test")),
		":robustirc.net 412 sECuRE :No text to send")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG #toast :foo")),
		":robustirc.net 403 sECuRE #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG #test foo")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG #test :foo")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG")),
		":robustirc.net 411 sECuRE :No recipient given (PRIVMSG)")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG sorcix :foo")),
		":robustirc.net 401 sECuRE sorcix :No such nick/channel")

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #NoExternalMessages"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #NoExternalMessages +n"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("PRIVMSG #NoExternalMessages :foo")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae PRIVMSG #NoExternalMessages :foo")
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PRIVMSG #NoExternalMessages :foo")),
		":robustirc.net 404 sECuRE #NoExternalMessages :Cannot send to channel")
}

func TestInvalidNotice(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE #test")),
		":robustirc.net 412 sECuRE :No text to send")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE #toast :foo")),
		":robustirc.net 403 sECuRE #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE #test foo")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad NOTICE #test :foo")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE")),
		":robustirc.net 411 sECuRE :No recipient given (NOTICE)")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE sorcix :foo")),
		":robustirc.net 401 sECuRE sorcix :No such nick/channel")

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #NoExternalMessages"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #NoExternalMessages +n"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("NOTICE #NoExternalMessages :foo")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae NOTICE #NoExternalMessages :foo")
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NOTICE #NoExternalMessages :foo")),
		":robustirc.net 404 sECuRE #NoExternalMessages :Cannot send to channel")
}
