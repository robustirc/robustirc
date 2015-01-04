package types

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/sorcix/irc"
)

type FancyId struct {
	Id    int64
	Reply int64
}

func (i *FancyId) String() string {
	return fmt.Sprintf("%d.%d", i.Id, i.Reply)
}

type FancyType int64

const (
	FancyCreateSession = iota
	FancyDeleteSession
	FancyIRCFromClient
	FancyIRCToClient
	FancyPing
)

type FancyMessage struct {
	Id      FancyId
	Session FancyId
	Type    FancyType
	Data    string

	// List of all servers currently in the network. Only present when Type == FancyPing.
	Servers []string `json:",omitempty"`

	// Current master, as a hint for the proxy (may save one redirect).
	Currentmaster string `json:",omitempty"`
}

func (m *FancyMessage) Timestamp() string {
	return time.Unix(0, m.Id.Id).Format("2006-01-02 15:04:05 -07:00")
}

func (m *FancyMessage) PrivacyFilter() string {
	if m.Type != FancyIRCToClient && m.Type != FancyIRCFromClient {
		return m.Data
	}
	if msg := irc.ParseMessage(m.Data); msg.Command == irc.PRIVMSG {
		msg.Trailing = "<privacy filtered>"
		return string(msg.Bytes())
	}
	return m.Data
}

func NewFancyMessage(t FancyType, session FancyId, data string) *FancyMessage {
	return &FancyMessage{
		// TODO(secure): bring in something else than just the time. Perhaps we
		// can put in the log index so that resumes are faster?
		Id:      FancyId{Id: time.Now().UnixNano()},
		Session: session,
		Type:    t,
		Data:    data,
	}
}

func NewFancyMessageFromBytes(b []byte) FancyMessage {
	var msg FancyMessage
	if err := json.Unmarshal(b, &msg); err != nil {
		log.Fatalf("Could not json.Unmarshal() a (supposed) FancyMessage (%v): %v\n", b, err)
	}
	return msg
}
