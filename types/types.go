package types

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/sorcix/irc"
)

type RobustId struct {
	Id    int64
	Reply int64
}

func (i *RobustId) String() string {
	return fmt.Sprintf("%d.%d", i.Id, i.Reply)
}

type RobustType int64

const (
	RobustCreateSession = iota
	RobustDeleteSession
	RobustIRCFromClient
	RobustIRCToClient
	RobustPing
	RobustMessageOfDeath
)

type RobustMessage struct {
	Id      RobustId
	Session RobustId
	Type    RobustType
	Data    string

	// InterestingFor is a map from session ids (only the Id part of a
	// RobustId, since Reply is always unset for sessions) to a bool that
	// signals whether the session is interested in the message.
	// InterestingFor gets set once in SendMessages and stays constant.
	InterestingFor map[int64]bool `json:"-"`

	// List of all servers currently in the network. Only present when Type == RobustPing.
	Servers []string `json:",omitempty"`

	// Current master, as a hint for the proxy (may save one redirect).
	Currentmaster string `json:",omitempty"`

	// ClientMessageId sent by client. Only present when Type == RobustIRCFromClient
	ClientMessageId uint64 `json:",omitempty"`
}

func (m *RobustMessage) Timestamp() string {
	return time.Unix(0, m.Id.Id).Format("2006-01-02 15:04:05 -07:00")
}

func (m *RobustMessage) PrivacyFilter() string {
	if m.Type != RobustIRCToClient && m.Type != RobustIRCFromClient {
		return m.Data
	}
	if msg := irc.ParseMessage(m.Data); msg.Command == irc.PRIVMSG {
		msg.Trailing = "<privacy filtered>"
		return string(msg.Bytes())
	}
	return m.Data
}

func NewRobustMessage(t RobustType, session RobustId, data string) *RobustMessage {
	return &RobustMessage{
		// TODO(secure): bring in something else than just the time. Perhaps we
		// can put in the log index so that resumes are faster?
		Id:      RobustId{Id: time.Now().UnixNano()},
		Session: session,
		Type:    t,
		Data:    data,
	}
}

func NewRobustMessageFromBytes(b []byte) RobustMessage {
	var msg RobustMessage
	if err := json.Unmarshal(b, &msg); err != nil {
		log.Fatalf("Could not json.Unmarshal() a (supposed) RobustMessage (%v): %v\n", b, err)
	}
	return msg
}
