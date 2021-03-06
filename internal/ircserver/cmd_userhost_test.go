package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestUserhost(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("OPER mero foo"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("USERHOST n")),
		":robustirc.net 302 sECuRE :")

	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("AWAY :gone"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("USERHOST secure xeen mero")),
		":robustirc.net 302 sECuRE :sECuRE*=+sECuRE!blah@robust/0x13b5aa0a2bcfb8ad xeen=-xeen!baz@robust/0x13b5aa0a2bcfb8af mero=+mero!foo@robust/0x13b5aa0a2bcfb8ae")
}
