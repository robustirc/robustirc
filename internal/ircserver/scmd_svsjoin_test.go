package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestServerSvsjoin(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":NickServ SVSJOIN bleh #test")),
		irc.ParseMessage(":robustirc.net 401 NickServ bleh :No such nick/channel"))

	msg := irc.ParseMessage(":NickServ SVSJOIN xeen #TEST")
	reply := i.ProcessMessage(&robust.Message{Session: ids["services"]}, msg)
	msgs := robustMessagesFromReply(reply)

	mustMatchIrcmsgs(t,
		&Replyctx{Messages: msgs},
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchInterestedMsgs(t, i,
		msg, []*robust.Message{msgs[0]},
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*robust.Message{msgs[1]},
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{false, false, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*robust.Message{msgs[2]},
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":NickServ SVSJOIN xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#bar"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #bar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #bar :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":NickServ SVSPART xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af PART #bar"),
		})

	mustMatchInterested(t, i,
		ids["services"], irc.ParseMessage(":NickServ SVSPART xeen #test"),
		[]robust.Id{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{true, false, true, true})

}
