package ircserver

import (
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func TestMotd(t *testing.T) {
	var got *Replyctx

	i, _ := stdIRCServer()

	idSecure := robust.Id{Id: 1420228218166687917}

	i.CreateSession(idSecure, "auth-secure", time.Unix(0, int64(idSecure.Id)))

	i.ProcessMessage(&robust.Message{Session: idSecure}, irc.ParseMessage("USER 1 2 3 :4"))
	got = i.ProcessMessage(&robust.Message{Session: idSecure}, irc.ParseMessage("NICK s[E]CuRE"))
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
