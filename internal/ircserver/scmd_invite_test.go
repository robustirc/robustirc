package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestServerInvite(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 ChanServ mero #test"),
			irc.ParseMessage(":ChanServ!services@services INVITE mero :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :ChanServ invited mero into the channel."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage(":ChanServ INVITE moro #test")),
		":robustirc.net 401 ChanServ moro :No such nick/channel")

	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		":robustirc.net 443 ChanServ mero #test :is already on channel")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")
}
