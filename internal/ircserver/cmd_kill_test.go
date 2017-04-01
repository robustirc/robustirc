package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestInterestedInKill(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("OPER mero foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 mero :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE mero :+o"),
		})

	msg := irc.ParseMessage("KILL secure :bleh")
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	replies := i.ProcessMessage(msgid, ids["mero"], msg)
	msgs := robustMessagesFromReply(replies)

	mustMatchIrcmsgs(t,
		replies,
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed by mero: bleh"),
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae KILL sECuRE :ircd!robust/0x13b5aa0a2bcfb8ae!mero (bleh)"),
			irc.ParseMessage("ERROR :Closing Link: sECuRE[robust/0x13b5aa0a2bcfb8ad] (Killed (mero (bleh)))"),
		})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[0]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[1]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[2]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, false})
}
