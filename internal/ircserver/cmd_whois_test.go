package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestIdle(t *testing.T) {
	i, ids := stdIRCServer()

	joinTime := time.Now()
	msg := robust.Message{
		UnixNano: joinTime.UnixNano(),
		Session:  ids["mero"],
		Data:     "JOIN #test",
	}
	if err := i.UpdateLastClientMessageID(&msg); err != nil {
		t.Fatalf("Unexpected error calling UpdateLastClientMessageID: %v", err)
	}
	i.ProcessMessage(&robust.Message{Id: msg.Id, Session: ids["mero"]}, irc.ParseMessage(string(msg.Data)))
	sMero, _ := i.GetSession(ids["mero"])
	if got, want := sMero.LastNonPing, joinTime; got != want {
		t.Fatalf("LastActivity for mero: got %v, want %v", got, want)
	}
	if got, want := sMero.LastActivity, joinTime; got != want {
		t.Fatalf("LastActivity for mero: got %v, want %v", got, want)
	}

	pingTime := time.Now()
	msg = robust.Message{
		UnixNano: pingTime.UnixNano(),
		Session:  ids["mero"],
		Data:     "PING :foo",
	}
	if err := i.UpdateLastClientMessageID(&msg); err != nil {
		t.Fatalf("Unexpected error calling UpdateLastClientMessageID: %v", err)
	}
	i.ProcessMessage(&robust.Message{Id: msg.Id, Session: ids["mero"]}, irc.ParseMessage(string(msg.Data)))
	if got, want := sMero.LastNonPing, joinTime; got != want {
		t.Fatalf("LastActivity for mero: got %v, want %v", got, want)
	}
	if got, want := sMero.LastActivity, pingTime; got != want {
		t.Fatalf("LastActivity for mero: got %v, want %v", got, want)
	}
}

func TestWhois(t *testing.T) {
	i, ids := stdIRCServer()

	mustMatchMsg(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS bero")),
		":robustirc.net 401 sECuRE bero :No such nick/channel")

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("OPER mero foo"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("AWAY :cleaning dishes"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #second"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #test"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #second"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("MODE #test +s"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("PART #test"))
	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("OPER mero foo"))

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})

	sMero, _ := i.GetSession(ids["mero"])
	sMero.modes['r'] = true

	mustMatchIrcmsgs(t,
		i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("WHOIS mero")),
		[]*irc.Message{
			irc.ParseMessage(":robustirc.net 311 sECuRE mero foo robust/0x13b5aa0a2bcfb8ae * :Axel Wagner"),
			irc.ParseMessage(":robustirc.net 319 sECuRE mero :#second @#test"),
			irc.ParseMessage(":robustirc.net 312 sECuRE mero robustirc.net :RobustIRC"),
			irc.ParseMessage(":robustirc.net 313 sECuRE mero :is an IRC operator"),
			irc.ParseMessage(":robustirc.net 301 sECuRE mero :cleaning dishes"),
			irc.ParseMessage(":robustirc.net 317 sECuRE mero 0 1420228218 :seconds idle, signon time"),
			irc.ParseMessage(":robustirc.net 307 sECuRE mero :user has identified to services"),
			irc.ParseMessage(":robustirc.net 318 sECuRE mero :End of /WHOIS list"),
		})
}
