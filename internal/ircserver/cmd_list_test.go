package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestList(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		":robustirc.net 323 sECuRE :End of LIST")

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 1 :"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :this is a topic"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 1 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #new"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #new 1 :"),
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST #test,#new")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 322 sECuRE #new 1 :"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST invalid,#test,invalid")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #new +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("LIST")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 322 sECuRE #test 2 :this is a topic"),
			irc.ParseMessage(":robustirc.net 323 sECuRE :End of LIST"),
		})
}
