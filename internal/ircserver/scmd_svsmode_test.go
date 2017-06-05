package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestServerSvsmode(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE secure")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage("SVSMODE secure +r")),
		":services.robustirc.net MODE sECuRE :+r")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage("SVSMODE socoro +r")),
		":robustirc.net 401 * socoro :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage("SVSMODE secure +rq")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 501 * :Unknown MODE flag"),
			irc.ParseMessage(":services.robustirc.net MODE sECuRE :+r"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage("SVSMODE secure +d-r")),
		":services.robustirc.net MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage("SVSMODE secure d-r")),
		":robustirc.net 501 * :Unknown MODE flag")
}
