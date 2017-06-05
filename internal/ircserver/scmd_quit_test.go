package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestServerQuit(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":services.robustirc.net NICK blorgh 1 1425542735 enforcer services.robustirc.net services.robustirc.net 0 :Services Enforcer"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["services"]}, irc.ParseMessage(":blorgh QUIT")),
		":blorgh!enforcer@robust/0x13c6cdee3e749faf QUIT :")
}
