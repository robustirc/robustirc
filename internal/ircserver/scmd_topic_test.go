package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerModeTopic(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +tr")),
		":ChanServ!services@services MODE #test +tr")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #toast +tr")),
		":robustirc.net 403 ChanServ #toast :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test -r+o secure")),
		":ChanServ!services@services MODE #test +o-r secure")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test -o secure")),
		":ChanServ!services@services MODE #test -o secure")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +o mero")),
		":robustirc.net 441 ChanServ mero #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :locked")),
		":ChanServ!services@services TOPIC #test :locked")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ abc :locked")),
		`:robustirc.net 461 * #test :Could not parse timestamp: strconv.ParseInt: parsing "abc": invalid syntax`)

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #toast ChanServ 0 :locked")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :")),
		":ChanServ!services@services TOPIC #test :")
}
