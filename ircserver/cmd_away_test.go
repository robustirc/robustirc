package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestAway(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("PRIVMSG mero :hey")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :hey")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("AWAY :upgrading server")),
		":robustirc.net 306 mero :You have been marked as being away")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("PRIVMSG mero :you there?")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad PRIVMSG mero :you there?"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :upgrading server"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("AWAY")),
		":robustirc.net 305 mero :You are no longer marked as being away")
}
