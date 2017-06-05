package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestWho(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHO")),
		":robustirc.net 315 sECuRE :End of /WHO list")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHO #nonexistant")),
		":robustirc.net 315 sECuRE #nonexistant :End of /WHO list")

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))
	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("AWAY :afk"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NICK secore"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net secore G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("join #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 mero #test foo robust/0x13b5aa0a2bcfb8ae robustirc.net mero H :0 Axel Wagner"),
			irc.ParseMessage(":robustirc.net 352 mero #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net secore G :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 352 mero #test baz robust/0x13b5aa0a2bcfb8af robustirc.net xeen H :0 Iks Enn"),
			irc.ParseMessage(":robustirc.net 315 mero #test :End of /WHO list"),
		})
}
