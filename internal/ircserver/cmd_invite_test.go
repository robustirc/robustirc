package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestInterestedInInvite(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	msg := irc.ParseMessage("INVITE xeen #test")
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(msgid, ids["secure"], msg)
	msgs := robustMessagesFromReply(replies)

	mustMatchIrcmsgs(t,
		&Replyctx{Messages: msgs},
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 sECuRE xeen #test"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad INVITE xeen :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :sECuRE invited xeen into the channel."),
		})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[0]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[1]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[2]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, true, false})
}

func TestInvite(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("INVITE mero #test")),
		":robustirc.net 442 sECuRE #test :You're not on that channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad JOIN :#test"),
			irc.ParseMessage(":robustirc.net MODE #test +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #test +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("INVITE mero #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("INVITE mero #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 sECuRE mero #test"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad INVITE mero :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :sECuRE invited mero into the channel."),
		})

	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("INVITE secure #test")),
		":robustirc.net 443 mero sECuRE #test :is already on channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("INVITE xeen #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 mero xeen #test"),
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae INVITE xeen :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :mero invited xeen into the channel."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("INVITE xoon #test")),
		":robustirc.net 401 mero xoon :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("MODE #test +i")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +i")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("INVITE xeen #test")),
		":robustirc.net 482 mero #test :You're not channel operator")

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #second"))
	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("MODE #second +i"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #second")),
		":robustirc.net 473 mero #second :Cannot join channel (+i)")

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("INVITE mero #second"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #second")),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae JOIN :#second"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #second :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #second +int"),
			irc.ParseMessage(":robustirc.net 331 mero #second :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #second :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #second :End of /NAMES list."),
		})

	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("AWAY :gone"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("INVITE xeen #second")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 sECuRE xeen #second"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad INVITE xeen :#second"),
			irc.ParseMessage(":robustirc.net NOTICE #second :sECuRE invited xeen into the channel."),
			irc.ParseMessage(":robustirc.net 301 sECuRE xeen :gone"),
		})

	// Verify INVITEs only work once.
	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #third"))
	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("MODE #third +i"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #third")),
		":robustirc.net 473 mero #third :Cannot join channel (+i)")

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("INVITE mero #third"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #third")),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae JOIN :#third"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #third :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #third +int"),
			irc.ParseMessage(":robustirc.net 331 mero #third :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 mero = #third :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #third :End of /NAMES list."),
		})

	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("PART #third"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #third")),
		":robustirc.net 473 mero #third :Cannot join channel (+i)")
}
