package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerJoinPart(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :ChanServ"))
	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK NickServ 1 1422134861 services robustirc.net services.robustirc.net 0 :NickServ"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":robustirc.net 442 ChanServ #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN #test")),
		":ChanServ!services@services JOIN :#test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net ChanServ H :0 ChanServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ JOIN #test")),
		":NickServ!services@services JOIN :#test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net ChanServ H :0 ChanServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net NickServ H :0 NickServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":ChanServ!services@services PART #test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net NickServ H :0 NickServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN !")),
		":robustirc.net 403 ChanServ ! :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN #new")),
		":ChanServ!services@services JOIN :#new")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #new")),
		":ChanServ!services@services PART #new")
}
