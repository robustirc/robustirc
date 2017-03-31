package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestKick(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))
	sXeen, _ := i.GetSession(ids["xeen"])

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("KICK #test secure :bye")),
		":robustirc.net 482 mero #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("KICK #test secure :bye")),
		":robustirc.net 442 xeen #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("KICK #toast secure :bye")),
		":robustirc.net 403 xeen #toast :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("KICK #test moro :bye bye")),
		":robustirc.net 441 sECuRE moro #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("KICK #test mero :bye bye")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad KICK #test mero :bye bye")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			{Prefix: &sXeen.ircPrefix, Command: irc.JOIN, Params: []string{"#TEST"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})
}
