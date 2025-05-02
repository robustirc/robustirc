package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestChannelMode(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test -nt"))
	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	// Set a topic from the outside.
	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :foobar")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :foobar"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :foobar"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +t")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test")),
		":robustirc.net 324 sECuRE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :bleh")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("TOPIC #test :bleh")),
		[]*irc.Message{
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae TOPIC #test :bleh"),
			irc.ParseMessage(":mero TOPIC #test mero 1420228218 :bleh"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :")),
		":robustirc.net 482 sECuRE #test :You're not channel operator")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +o sECuRE")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +o sECuRE")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +o nobody")),
		":robustirc.net 441 mero nobody #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +y sECuRE")),
		":robustirc.net 472 mero y :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #nonexistant +y sECuRE")),
		":robustirc.net 442 mero #nonexistant :You're not on that channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :finally")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :finally"),
		})
}

func TestUserMode(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE sECuRE +i")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+i")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE sECuRE -i")),
		":robustirc.net 502 xeen :Can't change mode for other users")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("OPER xeen foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 xeen :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE xeen :+o"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE sECuRE -i")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE sECuRE :-i")
}

func TestBans(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	// Bans are not yet implemented.
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test b")),
		":robustirc.net 368 sECuRE #test :End of Channel Ban List")
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +b")),
		":robustirc.net 368 sECuRE #test :End of Channel Ban List")
}

func TestKey(t *testing.T) {
	i, ids := stdIRCServer()

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +k")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE #test +k")
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test +k 1234")),
		":robustirc.net MODE #test +k 1234")
	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE #test -k")),
		":robustirc.net MODE #test -k 1234")
}

func TestChannelMemberStatus(t *testing.T) {
	i, ids := stdIRCServer()

	sSecure, _ := i.GetSession(ids["secure"])

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test"))
	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test")),
		[]*irc.Message{
			{Prefix: &sSecure.ircPrefix, Command: irc.JOIN, Params: []string{"#test"}},
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :sECuRE"),
			irc.ParseMessage(":robustirc.net 324 sECuRE #test +nt"),
			irc.ParseMessage(":robustirc.net 331 sECuRE #test :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 sECuRE = #test :@mero sECuRE"),
			irc.ParseMessage(":robustirc.net 366 sECuRE #test :End of /NAMES list."),
		})

	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +t")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +t")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +o xeen")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae MODE #test +o xeen")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE #test -o+o xeen sECuRE")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE #test +o-o sECuRE xeen")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE #test +o xeen")),
		":robustirc.net 482 xeen #test :You're not channel operator")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("TOPIC #test :finally")),
		[]*irc.Message{
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad TOPIC #test :finally"),
			irc.ParseMessage(":sECuRE TOPIC #test sECuRE 1420228218 :finally"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("TOPIC #test :nooo")),
		":robustirc.net 482 xeen #test :You're not channel operator")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("OPER xeen foo")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 381 xeen :You are now an IRC operator"),
			irc.ParseMessage(":robustirc.net MODE xeen :+o"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE xeen")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE xeen :+o")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("MODE #test +o xeen")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af MODE #test +o xeen")
}

func TestInvisibleMessage(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("PRIVMSG sECuRE :before invisible")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af PRIVMSG sECuRE :before invisible")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE sECuRE +G")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+G")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("PRIVMSG sECuRE :after invisible")),
		nil)

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("NOTICE sECuRE :after invisible")),
		nil)

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #common"))
	i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #common"))

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("PRIVMSG sECuRE :common channel")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af PRIVMSG sECuRE :common channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("NOTICE sECuRE :common channel")),
		":xeen!baz@robust/0x13b5aa0a2bcfb8af NOTICE sECuRE :common channel")

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("MODE sECuRE -G")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :-G")
}

func TestDefaultChannelModes(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["xeen"]}, irc.ParseMessage("JOIN #foobar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#foobar"),
			irc.ParseMessage(":robustirc.net MODE #foobar +nt"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #foobar :@xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #foobar +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #foobar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #foobar :@xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #foobar :End of /NAMES list."),
		})
}
