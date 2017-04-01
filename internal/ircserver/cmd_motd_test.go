package ircserver

import (
	"testing"

	"github.com/robustirc/robustirc/types"

	"gopkg.in/sorcix/irc.v2"
)

func TestMotd(t *testing.T) {
	var got *Replyctx

	i, _ := stdIRCServer()

	idSecure := types.RobustId{Id: 1420228218166687917}

	i.CreateSession(idSecure, "auth-secure")

	i.ProcessMessage(types.RobustId{}, idSecure, irc.ParseMessage("USER 1 2 3 :4"))
	got = i.ProcessMessage(types.RobustId{}, idSecure, irc.ParseMessage("NICK s[E]CuRE"))
	motdFound := false
	for i := 0; i < len(got.Messages)-2; i++ {
		if irc.ParseMessage(got.Messages[i].Data).Command == irc.RPL_MOTDSTART &&
			irc.ParseMessage(got.Messages[i+1].Data).Command == irc.RPL_MOTD &&
			irc.ParseMessage(got.Messages[i+2].Data).Command == irc.RPL_ENDOFMOTD {
			motdFound = true
			break
		}
	}
	if !motdFound {
		t.Fatalf("got %v, did not find MOTDSTART, MOTD, ENDOFMOTD in order", got)
	}
}
