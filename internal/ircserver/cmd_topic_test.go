package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

// TestTopic tests that setting/getting topics works properly. Using topic as
// the channel’s state, it also verifies that joining/parting will
// create/delete channels.
func TestTopic(t *testing.T) {
	i, ids := stdIRCServer()

	sSecure, _ := i.GetSession(ids["secure"])
	sMero, _ := i.GetSession(ids["mero"])

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("TOPIC #nonexistant")),
		":robustirc.net 403 sECuRE #nonexistant :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 331 sECuRE #test :No topic is set")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic.")),
		[]*irc.Message{
			{
				Prefix:  &sSecure.ircPrefix,
				Command: irc.TOPIC,
				Params:  []string{"#test", "yeah, this is a topic."},
			},
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :yeah, this is a topic."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("TOPIC #test :")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 0 :"),
		})

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("TOPIC #test :yeah, this is a topic."))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			{Prefix: &sMero.ircPrefix, Command: irc.JOIN, Params: []string{"#test"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :mero"),
			irc.ParseMessage(":robustirc.net 324 mero #test +nt"),
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE 1420228218"),
			irc.ParseMessage(":robustirc.net 353 mero = #test :@sECuRE mero"),
			irc.ParseMessage(":robustirc.net 366 mero #test :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("TOPIC #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 332 mero #test :yeah, this is a topic."),
			irc.ParseMessage(":robustirc.net 333 mero #test sECuRE 1420228218"),
		})

	mustMatchIrcmsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	mustMatchIrcmsg(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sSecure.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("PART #test")),
		":robustirc.net 403 sECuRE #test :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("TOPIC #test")),
		":robustirc.net 403 mero #test :No such channel")

	// Same with QUIT
	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test")),
		&irc.Message{Prefix: &sMero.ircPrefix, Command: irc.PART, Params: []string{"#test"}})

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 442 mero #test :You're not on that channel")

	i.ProcessMessage(robust.Id{}, ids["secure"], irc.ParseMessage("QUIT"))

	mustMatchMsg(t,
		i.ProcessMessage(robust.Id{}, ids["mero"], irc.ParseMessage("PART #test")),
		":robustirc.net 403 mero #test :No such channel")
}
