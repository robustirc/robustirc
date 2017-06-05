package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestServerPrivmsg(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG secure :ohai")),
		":ChanServ!services@services PRIVMSG secure :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG socoro :ohai")),
		":robustirc.net 401 ChanServ socoro :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG #test :ohai")),
		":ChanServ!services@services PRIVMSG #test :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG")),
		":robustirc.net 411 ChanServ :No recipient given (PRIVMSG)")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG #test")),
		":ChanServ!services@services PRIVMSG #test :#test")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ PRIVMSG #toast :a")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":ChanServ NOTICE")),
		":robustirc.net 411 ChanServ :No recipient given (NOTICE)")
}
