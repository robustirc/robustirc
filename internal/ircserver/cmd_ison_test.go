package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestIson(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("ISON")),
		":robustirc.net 461 xeen ISON :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("ISON mero sECuRE nope")),
		":robustirc.net 303 xeen :mero sECuRE")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["xeen"], irc.ParseMessage("ISON nope nada nein")),
		":robustirc.net 303 xeen :")
}
