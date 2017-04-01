package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerSvsnick(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad NICK :socoro")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":robustirc.net 401 * secure :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure ! :1")),
		":robustirc.net 432 * ! :Erroneous nickname")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@socoro xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchInterested(t, i,
		ids["services"], irc.ParseMessage("SVSNICK socoro sucuru :1"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, true})
}
