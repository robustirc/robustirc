package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestKnock(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test2"))
	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("MODE #test +i"))

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK")),
		":robustirc.net 461 xeen KNOCK :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK #test3")),
		":robustirc.net 480 xeen :Cannot knock on #test3 (Channel does not exist)")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK #test2")),
		":robustirc.net 480 xeen :Cannot knock on #test2 (Channel is not invite only)")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net NOTICE #test :[Knock] by xeen!baz@robust/0x13b5aa0a2bcfb8af (no reason specified)"),
			irc.ParseMessage(":robustirc.net NOTICE xeen :Knocked on #test"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK #test :foobar baz")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net NOTICE #test :[Knock] by xeen!baz@robust/0x13b5aa0a2bcfb8af (foobar baz)"),
			irc.ParseMessage(":robustirc.net NOTICE xeen :Knocked on #test"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("KNOCK #test foobar baz")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net NOTICE #test :[Knock] by xeen!baz@robust/0x13b5aa0a2bcfb8af (foobar baz)"),
			irc.ParseMessage(":robustirc.net NOTICE xeen :Knocked on #test"),
		})
}
