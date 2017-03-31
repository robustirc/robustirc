package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestNames(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NAMES")),
		":robustirc.net 366 sECuRE * :End of /NAMES list.")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NAMES #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})
}
