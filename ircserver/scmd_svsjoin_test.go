package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerSvsjoin(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSJOIN bleh #test")),
		irc.ParseMessage(":robustirc.net 401 NickServ bleh :No such nick/channel"))

	msg := irc.ParseMessage(":NickServ SVSJOIN xeen #TEST")
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	reply := i.ProcessMessage(msgid, ids["services"], msg)
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
		msg, []*types.RobustMessage{msgs[0]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[1]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{false, false, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[2]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSJOIN xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#bar"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #bar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #bar :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSPART xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af PART #bar"),
		})

	mustMatchInterested(t, i,
		ids["services"], irc.ParseMessage(":NickServ SVSPART xeen #test"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{true, false, true, true})

}
