package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func TestServerSvshold(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	now := time.Now()

	serverSession, _ := i.GetSession(ids["services"])
	serverSession.LastActivity = now

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSHOLD newnick 5 :held by services")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NICK newnick")),
		":robustirc.net 432 sECuRE newnick :Erroneous Nickname: held by services")

	s, _ := i.GetSession(ids["secure"])
	s.LastActivity = now.Add(10 * time.Second)

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NICK newnick")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad NICK :newnick")

	now = time.Now()

	serverSession.LastActivity = now
	s.LastActivity = now

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSHOLD anothernick 5 :held by services")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NICK anothernick")),
		":robustirc.net 432 newnick anothernick :Erroneous Nickname: held by services")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSHOLD anothernick")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NICK anothernick")),
		":newnick!blah@robust/0x13b5aa0a2bcfb8ad NICK :anothernick")
}
