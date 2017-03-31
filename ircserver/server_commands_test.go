package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func stdIRCServerWithServices() (*IRCServer, map[string]types.RobustId) {
	i, ids := stdIRCServer()
	i.Config.IRC.Services = append(i.Config.IRC.Services, config.Service{
		Password: "mypass",
	})
	ids["services"] = types.RobustId{Id: 0x13c6cdee3e749faf}
	i.CreateSession(ids["services"], "auth-server")
	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("PASS :services=mypass"))
	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks"))
	return i, ids
}

func TestServerHandshake(t *testing.T) {
	i, ids := stdIRCServer()
	i.Config.IRC.Services = append(i.Config.IRC.Services, config.Service{
		Password: "mypass",
	})

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("OPER mero foo"))
	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	ids["services"] = types.RobustId{Id: 0x13c6cdee3e749faf}

	i.CreateSession(ids["services"], "auth-server")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("PASS :services=wrong")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks")),
		"ERROR :Invalid password")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("PASS :services=mypass")),
		[]*irc.Message{})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks")),
		[]*irc.Message{
			irc.ParseMessage("SERVER robustirc.net 1 23"),
			irc.ParseMessage("NICK mero 1 1 foo robust/0x13b5aa0a2bcfb8ae robustirc.net 0 + :Axel Wagner"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #test :@mero"),
			irc.ParseMessage("NICK sECuRE 1 1 blah robust/0x13b5aa0a2bcfb8ad robustirc.net 0 +o :Michael Stapelberg"),
			irc.ParseMessage("NICK xeen 1 1 baz robust/0x13b5aa0a2bcfb8af robustirc.net 0 + :Iks Enn"),
		})
}

func TestServerSjoin(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("SJOIN 1 #test :ChanServ")),
		":robustirc.net 421 sECuRE SJOIN :Unknown command")
}

func TestServerKickKill(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :ChanServ"))
	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK NickServ 1 1422134861 services robustirc.net services.robustirc.net 0 :NickServ"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ KICK #test sECuRE :bye")),
		":ChanServ!services@services KICK #test sECuRE :bye")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ KICK #test sECuRE :bye")),
		":robustirc.net 441 ChanServ sECuRE #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ KICK #toast sECuRE :bye")),
		":robustirc.net 403 ChanServ #toast :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :mero xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ KILL mero")),
		":robustirc.net 461 * KILL :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ KILL you :nope")),
		":robustirc.net 401 * you :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ KILL mero :Too many wrong passwords")),
		[]*irc.Message{
			irc.ParseMessage(":NickServ!services@robust/0x13c6cdee3e749faf KILL mero :ircd!robust/0x13c6cdee3e749faf!NickServ (Too many wrong passwords)"),
			irc.ParseMessage(":mero!foo@robust/0x13b5aa0a2bcfb8ae QUIT :Killed: Too many wrong passwords"),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":services.robustirc.net KILL secure :Too many wrong passwords")),
		[]*irc.Message{
			irc.ParseMessage(":services.robustirc.net KILL sECuRE :ircd!services.robustirc.net (Too many wrong passwords)"),
			irc.ParseMessage(":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad QUIT :Killed: Too many wrong passwords"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ KICK #test xeen :bye")),
		":ChanServ!services@services KICK #test xeen :bye")
}
