package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestNames(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NAMES")),
		":robustirc.net 366 sECuRE * :End of /NAMES list.")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NAMES #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})
}

func TestNamesInvisible(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NAMES #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@xeen"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})

	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE xeen +i"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NAMES #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NAMES #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@xeen sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})
}
