package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerQuit(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":services.robustirc.net NICK blorgh 1 1425542735 enforcer services.robustirc.net services.robustirc.net 0 :Services Enforcer"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":blorgh QUIT")),
		":blorgh!enforcer@robust/0x13c6cdee3e749faf QUIT :")
}
