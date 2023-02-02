package main

import (
	"context"
	"testing"
	"time"

	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/outputstream"
	"github.com/robustirc/robustirc/internal/robust"
	"gopkg.in/sorcix/irc.v2"
)

// TestPlumbing exercises the code paths for storing messages in outputstream
// and getting them from multiple sessions.
func TestPlumbing(t *testing.T) {
	i := ircserver.NewIRCServer("robustirc.net", time.Unix(0, 1481144012969203276))

	ids := make(map[string]robust.Id)

	ids["secure"] = robust.Id{Id: 1420228218166687917}
	ids["mero"] = robust.Id{Id: 1420228218166687918}

	i.CreateSession(ids["secure"], "auth-secure", time.Unix(0, int64(ids["secure"].Id)))
	i.CreateSession(ids["mero"], "auth-mero", time.Unix(0, int64(ids["mero"].Id)))

	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("NICK sECuRE"))
	i.ProcessMessage(&robust.Message{Session: ids["secure"]}, irc.ParseMessage("USER blah 0 * :Michael Stapelberg"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("NICK mero"))
	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("USER foo 0 * :Axel Wagner"))

	o, err := outputstream.NewOutputStream("")
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	msgid := robust.Id{Id: uint64(time.Now().UnixNano())}
	replies := i.ProcessMessage(&robust.Message{Id: msgid, Session: ids["secure"]}, irc.ParseMessage("JOIN #test"))
	sendMessages(replies, ids["secure"], msgid.Id, o)
	got, ok := o.Get(msgid)
	if !ok {
		t.Fatalf("_, ok := Get(%d); got false, want true", msgid.Id)
	}
	if len(got) != len(replies.Messages) {
		t.Fatalf("len(got): got %d, want %d", len(got), len(replies.Messages))
	}
	if got[0].Data != string(replies.Messages[0].Data) {
		t.Fatalf("message 0: got %v, want %v", got[0].Data, string(replies.Messages[0].Data))
	}

	nextid := robust.Id{Id: uint64(time.Now().UnixNano())}
	replies = i.ProcessMessage(&robust.Message{Id: nextid, Session: ids["secure"]}, irc.ParseMessage("JOIN #foobar"))
	sendMessages(replies, ids["secure"], nextid.Id, o)
	got = o.GetNext(context.TODO(), msgid)
	if !ok {
		t.Fatalf("_, ok := Get(%d); got false, want true", msgid.Id)
	}
	if len(got) != len(replies.Messages) {
		t.Fatalf("len(got): got %d, want %d", len(got), len(replies.Messages))
	}
	if got[0].Data != replies.Messages[0].Data {
		t.Fatalf("message 0: got %v, want %v", got[0].Data, replies.Messages[0].Data)
	}

	if got[0].InterestingFor[ids["mero"].Id] {
		t.Fatalf("sMero interestedIn JOIN to #foobar, expected false")
	}

	i.ProcessMessage(&robust.Message{Session: ids["mero"]}, irc.ParseMessage("JOIN #baz"))

	msgid = robust.Id{Id: uint64(time.Now().UnixNano())}
	replies = i.ProcessMessage(&robust.Message{Id: msgid, Session: ids["secure"]}, irc.ParseMessage("JOIN #baz"))
	sendMessages(replies, ids["secure"], msgid.Id, o)
	got, _ = o.Get(msgid)
	if !got[0].InterestingFor[ids["mero"].Id] {
		t.Fatalf("sMero not interestedIn JOIN to #baz, expected true")
	}
}
