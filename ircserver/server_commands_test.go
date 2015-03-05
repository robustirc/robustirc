package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
)

func stdIRCServerWithServices() (*IRCServer, map[string]types.RobustId) {
	i, ids := stdIRCServer()
	i.Config.Services = append(i.Config.Services, config.Service{
		Password: "mypass",
	})
	ids["services"] = types.RobustId{Id: 0x13c6cdee3e749faf}
	i.CreateSession(ids["services"], "auth-server")
	i.ProcessMessage(ids["services"], irc.ParseMessage("PASS :services=mypass"))
	i.ProcessMessage(ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks"))
	return i, ids
}

func TestServerHandshake(t *testing.T) {
	i, ids := stdIRCServer()
	i.Config.Services = append(i.Config.Services, config.Service{
		Password: "mypass",
	})

	i.ProcessMessage(ids["secure"], irc.ParseMessage("OPER mero foo"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	ids["services"] = types.RobustId{Id: 0x13c6cdee3e749faf}

	i.CreateSession(ids["services"], "auth-server")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("PASS :services=wrong")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks")),
		"ERROR :Invalid password")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("PASS :services=mypass")),
		[]*irc.Message{})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SERVER services.robustirc.net 1 :Services for IRC Networks")),
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
		i.ProcessMessage(ids["secure"], irc.ParseMessage("SJOIN 1 #test :ChanServ")),
		":robustirc.net 421 sECuRE SJOIN :Unknown command")
}

func TestServerKickKill(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ KICK #test sECuRE :bye")),
		":ChanServ!services@services KICK #test sECuRE :bye")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ KICK #test sECuRE :bye")),
		":robustirc.net 441 * sECuRE #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ KICK #toast sECuRE :bye")),
		":robustirc.net 403 * #toast :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :mero xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("KILL mero")),
		":robustirc.net 461 * KILL :Not enough parameters")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("KILL you :nope")),
		":robustirc.net 401 * you :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("KILL mero :Too many wrong passwords")),
		":mero!foo@robust/0x13b5aa0a2bcfb8ae QUIT :Killed: Too many wrong passwords")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ KICK #test xeen :bye")),
		":ChanServ!services@services KICK #test xeen :bye")
}

func TestServerSvsnick(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad NICK :socoro")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":robustirc.net 401 * secure :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSNICK secure ! :1")),
		":robustirc.net 432 * ! :Erroneous nickname")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@socoro xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})
}

func TestServerSvsmode(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("MODE secure")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSMODE secure +r")),
		":services.robustirc.net MODE sECuRE :+r")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSMODE socoro +r")),
		":robustirc.net 401 * socoro :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSMODE secure +rq")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 501 * :Unknown MODE flag"),
			irc.ParseMessage(":services.robustirc.net MODE sECuRE :+r"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSMODE secure +d-r")),
		":services.robustirc.net MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("SVSMODE secure d-r")),
		":robustirc.net 501 * :Unknown MODE flag")
}

func TestServerModeTopic(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test +tr")),
		":ChanServ!services@services MODE #test +tr")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #toast +tr")),
		":robustirc.net 403 ChanServ #toast :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test +q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test -r+o secure")),
		":ChanServ!services@services MODE #test -r+o secure")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test -o secure")),
		":ChanServ!services@services MODE #test -o secure")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ MODE #test +o mero")),
		":robustirc.net 441 ChanServ mero #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :locked")),
		":ChanServ!services@services TOPIC #test :locked")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ abc :locked")),
		`:robustirc.net 461 * #test :Could not parse timestamp: strconv.ParseInt: parsing "abc": invalid syntax`)

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ TOPIC #toast ChanServ 0 :locked")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :")),
		":ChanServ!services@services TOPIC #test :")
}

func TestServerNick(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server")),
		":robustirc.net 433 * ChanServ :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(ids["secure"], irc.ParseMessage("NICK NickServ")),
		":robustirc.net 433 * NickServ :Nickname is already in use")
}

func TestServerJoinPart(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":robustirc.net 442 ChanServ #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PART #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ JOIN #test")),
		":ChanServ!services@services JOIN :#test")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":ChanServ!services@services PART #test")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ JOIN !")),
		":robustirc.net 403 ChanServ ! :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ JOIN #new")),
		":ChanServ!services@services JOIN :#new")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PART #new")),
		":ChanServ!services@services PART #new")
}

func TestServerPrivmsg(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PRIVMSG secure :ohai")),
		":ChanServ!services@services PRIVMSG secure :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PRIVMSG socoro :ohai")),
		":robustirc.net 401 ChanServ socoro :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PRIVMSG #test :ohai")),
		":ChanServ!services@services PRIVMSG #test :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ PRIVMSG")),
		":robustirc.net 411 ChanServ :No recipient given (PRIVMSG)")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ NOTICE")),
		":robustirc.net 411 ChanServ :No recipient given (NOTICE)")
}

func TestServerInvite(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 ChanServ mero #test"),
			irc.ParseMessage(":ChanServ!services@services INVITE mero :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :ChanServ invited mero into the channel."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ INVITE moro #test")),
		":robustirc.net 401 ChanServ moro :No such nick/channel")

	i.ProcessMessage(ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		":robustirc.net 443 ChanServ mero #test :is already on channel")

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":ChanServ INVITE mero #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")
}

func TestServerQuit(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(ids["services"], irc.ParseMessage(":services.robustirc.net NICK blorgh 1 1425542735 enforcer services.robustirc.net services.robustirc.net 0 :Services Enforcer"))

	mustMatchMsg(t,
		i.ProcessMessage(ids["services"], irc.ParseMessage(":blorgh QUIT")),
		":blorgh!enforcer@robust/0x13c6cdee3e749faf QUIT")
}
