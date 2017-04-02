package robust

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/sorcix/irc.v2"
)

// XXX(1.0): replace MessageOffset with 7804071725000000000 (2217-04-20 23:42:05)
// MessageOffset will be added to all robust.Message ids. We need
// an offset because message ids must be monotonically increasing,
// and RobustIRC used to use UNIX nano timestamps. For new
// networks, the offset doesn’t hurt, and it’s configurable in
// case networks need to transition back and forth between the old
// and the new mechanism. See also issue #150.
var MessageOffset uint64

func IdFromRaftIndex(index uint64) uint64 {
	return MessageOffset + index
}

type Id struct {
	Id    int64
	Reply int64
}

func (i *Id) String() string {
	return fmt.Sprintf("%d.%d", i.Id, i.Reply)
}

type Type int64

const (
	CreateSession Type = iota
	DeleteSession
	IRCFromClient
	IRCToClient
	Ping
	MessageOfDeath
	Config
	State
	Any
)

func (t Type) String() string {
	switch t {
	case CreateSession:
		return "create_session"
	case DeleteSession:
		return "delete_session"
	case IRCFromClient:
		return "irc_from_client"
	case IRCToClient:
		return "irc_to_client"
	case Ping:
		return "ping"
	case MessageOfDeath:
		return "message_of_death"
	case Config:
		return "config"
	case State:
		return "state"
	case Any:
		return "any"
	default:
		log.Panicf("(robust.Type).String() not updated for type %d", t)
	}
	// unreached
	return ""
}

type Message struct {
	Id      Id
	Session Id
	Type    Type
	Data    string

	// InterestingFor is a map from session ids (only the Id part of a
	// robust.Id, since Reply is always unset for sessions) to a bool
	// that signals whether the session is interested in the message.
	// InterestingFor gets set once in SendMessages and stays
	// constant.
	InterestingFor map[int64]bool `json:"-"`

	// List of all servers currently in the network. Only present when
	// Type == robust.Ping.
	Servers []string `json:",omitempty"`

	// Current master, as a hint for the proxy (may save one redirect).
	Currentmaster string `json:",omitempty"`

	// ClientMessageId sent by client. Only present when Type ==
	// robust.IRCFromClient
	ClientMessageId uint64 `json:",omitempty"`

	// Revision is the config file revision. Only present when Type ==
	// robust.Config
	Revision uint64 `json:",omitempty"`
}

func (m *Message) Timestamp() string {
	return time.Unix(0, m.Id.Id).Format("2006-01-02 15:04:05 -07:00")
}

func (m *Message) PrivacyFilter() string {
	if m.Type != IRCToClient && m.Type != IRCFromClient {
		return m.Data
	}
	if msg := irc.ParseMessage(m.Data); msg != nil {
		command := strings.ToUpper(msg.Command)
		if command == irc.PRIVMSG ||
			command == irc.NOTICE ||
			command == irc.PASS ||
			strings.HasSuffix(command, "serv") {
			if len(msg.Params) > 0 {
				msg.Params[len(msg.Params)-1] = "<privacy filtered>"
			}
			return string(msg.Bytes())
		}
	}
	return m.Data
}

func NewMessageFromBytes(b []byte) Message {
	var msg Message
	if err := json.Unmarshal(b, &msg); err != nil {
		log.Panicf("Could not json.Unmarshal() a (supposed) robust.Message (%v): %v\n", b, err)
	}
	return msg
}
