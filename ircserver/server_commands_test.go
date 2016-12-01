package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
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

func TestServerSvsnick(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad NICK :socoro")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure socoro :1")),
		":robustirc.net 401 * secure :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSNICK secure ! :1")),
		":robustirc.net 432 * ! :Erroneous nickname")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["xeen"], irc.ParseMessage("JOIN #TEST")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 324 xeen #TEST +nt"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@socoro xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchInterested(t, i,
		ids["services"], irc.ParseMessage("SVSNICK socoro sucuru :1"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, true})
}

func TestServerSvsmode(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("MODE secure")),
		":sECuRE!blah@robust/0x13b5aa0a2bcfb8ad MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSMODE secure +r")),
		":services.robustirc.net MODE sECuRE :+r")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSMODE socoro +r")),
		":robustirc.net 401 * socoro :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSMODE secure +rq")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 501 * :Unknown MODE flag"),
			irc.ParseMessage(":services.robustirc.net MODE sECuRE :+r"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSMODE secure +d-r")),
		":services.robustirc.net MODE sECuRE :+")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("SVSMODE secure d-r")),
		":robustirc.net 501 * :Unknown MODE flag")
}

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

func TestServerModeTopic(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +tr")),
		":ChanServ!services@services MODE #test +tr")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #toast +tr")),
		":robustirc.net 403 ChanServ #toast :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test q blargh")),
		":robustirc.net 472 ChanServ q :is unknown mode char to me")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test -r+o secure")),
		":ChanServ!services@services MODE #test +o-r secure")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test -o secure")),
		":ChanServ!services@services MODE #test -o secure")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ MODE #test +o mero")),
		":robustirc.net 441 ChanServ mero #test :They aren't on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :locked")),
		":ChanServ!services@services TOPIC #test :locked")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ abc :locked")),
		`:robustirc.net 461 * #test :Could not parse timestamp: strconv.ParseInt: parsing "abc": invalid syntax`)

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #toast ChanServ 0 :locked")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ TOPIC #test ChanServ 0 :")),
		":ChanServ!services@services TOPIC #test :")
}

func TestServerNick(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server")),
		[]*irc.Message{})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :Operator Server")),
		":robustirc.net 433 * ChanServ :Nickname is already in use")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("NICK NickServ")),
		":robustirc.net 433 sECuRE NickServ :Nickname is already in use")
}

func TestServerJoinPart(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK ChanServ 1 1422134861 services robustirc.net services.robustirc.net 0 :ChanServ"))
	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage("NICK NickServ 1 1422134861 services robustirc.net services.robustirc.net 0 :NickServ"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":robustirc.net 442 ChanServ #test :You're not on that channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN #test")),
		":ChanServ!services@services JOIN :#test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net ChanServ H :0 ChanServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ JOIN #test")),
		":NickServ!services@services JOIN :#test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net ChanServ H :0 ChanServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net NickServ H :0 NickServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #test")),
		":ChanServ!services@services PART #test")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("WHO #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 352 sECuRE #test services robust/0x13c6cdee3e749faf robustirc.net NickServ H :0 NickServ"),
			irc.ParseMessage(":robustirc.net 352 sECuRE #test blah robust/0x13b5aa0a2bcfb8ad robustirc.net sECuRE H :0 Michael Stapelberg"),
			irc.ParseMessage(":robustirc.net 315 sECuRE #test :End of /WHO list"),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN !")),
		":robustirc.net 403 ChanServ ! :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ JOIN #new")),
		":ChanServ!services@services JOIN :#new")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PART #new")),
		":ChanServ!services@services PART #new")
}

func TestServerPrivmsg(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG secure :ohai")),
		":ChanServ!services@services PRIVMSG secure :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG socoro :ohai")),
		":robustirc.net 401 ChanServ socoro :No such nick/channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG #test :ohai")),
		":ChanServ!services@services PRIVMSG #test :ohai")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG")),
		":robustirc.net 411 ChanServ :No recipient given (PRIVMSG)")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG #test")),
		":robustirc.net 412 ChanServ :No text to send")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ PRIVMSG #toast :a")),
		":robustirc.net 403 ChanServ #toast :No such channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ NOTICE")),
		":robustirc.net 411 ChanServ :No recipient given (NOTICE)")
}

func TestServerInvite(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 341 ChanServ mero #test"),
			irc.ParseMessage(":ChanServ!services@services INVITE mero :#test"),
			irc.ParseMessage(":robustirc.net NOTICE #test :ChanServ invited mero into the channel."),
		})

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ INVITE moro #test")),
		":robustirc.net 401 ChanServ moro :No such nick/channel")

	i.ProcessMessage(types.RobustId{}, ids["mero"], irc.ParseMessage("JOIN #test"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #test")),
		":robustirc.net 443 ChanServ mero #test :is already on channel")

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":ChanServ INVITE mero #toast")),
		":robustirc.net 403 ChanServ #toast :No such channel")
}

func TestServerQuit(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":services.robustirc.net NICK blorgh 1 1425542735 enforcer services.robustirc.net services.robustirc.net 0 :Services Enforcer"))

	mustMatchMsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":blorgh QUIT")),
		":blorgh!enforcer@robust/0x13c6cdee3e749faf QUIT :")
}

func TestServerSvsjoin(t *testing.T) {
	i, ids := stdIRCServerWithServices()

	i.ProcessMessage(types.RobustId{}, ids["secure"], irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsg(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSJOIN bleh #test")),
		irc.ParseMessage(":robustirc.net 401 NickServ bleh :No such nick/channel"))

	msg := irc.ParseMessage(":NickServ SVSJOIN xeen #TEST")
	msgid := types.RobustId{Id: time.Now().UnixNano()}
	reply := i.ProcessMessage(msgid, ids["services"], msg)
	i.SendMessages(reply, ids["services"], msgid.Id)
	msgs, _ := i.Get(msgid)

	mustMatchIrcmsgs(t,
		&Replyctx{Messages: msgs},
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#TEST"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #TEST :xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #TEST :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #TEST :@sECuRE xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #TEST :End of /NAMES list."),
		})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[0]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{true, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[1]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{false, false, false, true})

	mustMatchInterestedMsgs(t, i,
		msg, []*types.RobustMessage{msgs[2]},
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"]},
		[]bool{false, false, true})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSJOIN xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af JOIN :#bar"),
			irc.ParseMessage(":robustirc.net SJOIN 1 #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 331 xeen #bar :No topic is set"),
			irc.ParseMessage(":robustirc.net 353 xeen = #bar :@xeen"),
			irc.ParseMessage(":robustirc.net 366 xeen #bar :End of /NAMES list."),
		})

	mustMatchIrcmsgs(t,
		i.ProcessMessage(types.RobustId{}, ids["services"], irc.ParseMessage(":NickServ SVSPART xeen #bar")),
		[]*irc.Message{
			irc.ParseMessage(":xeen!baz@robust/0x13b5aa0a2bcfb8af PART #bar"),
		})

	mustMatchInterested(t, i,
		ids["services"], irc.ParseMessage(":NickServ SVSPART xeen #test"),
		[]types.RobustId{ids["secure"], ids["mero"], ids["xeen"], ids["services"]},
		[]bool{true, false, true, true})

}
