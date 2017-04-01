package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestQuit(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("quit :bye bye")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :bye bye"),
			irc.ParseMessage("ERROR :Closing Link: sECuRE[robust/0x13b5aa0a2bcfb8ad] (bye bye)"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("part #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("join #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})
}
