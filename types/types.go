package types

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
	RobustConfig
	RobustAny
)

func (t RobustType) String() string {
	switch t {
	case RobustCreateSession:
		return "create_session"
	case RobustDeleteSession:
		return "delete_session"
	case RobustIRCFromClient:
		return "irc_from_client"
	case RobustIRCToClient:
		return "irc_to_client"
	case RobustPing:
		return "ping"
	case RobustMessageOfDeath:
		return "message_of_death"
	case RobustConfig:
		return "config"
	case RobustAny:
		return "any"
	default:
		log.Panicf("RobustType.String() not updated for type %d", t)
	}
	// unreached
	return ""
}

type RobustMessage struct {
	Id      RobustId
	Session RobustId
	Type    RobustType
	Data    string

	// InterestingFor is a map from session ids (only the Id part of a
	// RobustId, since Reply is always unset for sessions) to a bool that
	// signals whether the session is interested in the message.
	// InterestingFor gets set once in SendMessages and stays constant.
	InterestingFor       map[int64]bool  `json:"-"`
	InterestingForCanary map[string]bool `json:",omitempty"`

	// List of all servers currently in the network. Only present when Type == RobustPing.
	Servers []string `json:",omitempty"`

	// Current master, as a hint for the proxy (may save one redirect).
	Currentmaster string `json:",omitempty"`

	// ClientMessageId sent by client. Only present when Type == RobustIRCFromClient
	ClientMessageId uint64 `json:",omitempty"`

	// Revision is the config file revision. Only present when Type == RobustConfig
	Revision int `json:",omitempty"`
}

func (m *RobustMessage) Timestamp() string {
	return time.Unix(0, m.Id.Id).Format("2006-01-02 15:04:05 -07:00")
}

func (m *RobustMessage) PrivacyFilter() string {
	if m.Type != RobustIRCToClient && m.Type != RobustIRCFromClient {
		return m.Data
	}
	if msg := irc.ParseMessage(m.Data); msg != nil {
		command := strings.ToUpper(msg.Command)
		if command == irc.PRIVMSG ||
			command == irc.NOTICE ||
			strings.HasSuffix(command, "serv") {
			msg.Trailing = "<privacy filtered>"
			return string(msg.Bytes())
		}
	}
	return m.Data
}

func NewRobustMessageFromBytes(b []byte) RobustMessage {
	var msg RobustMessage
	if err := json.Unmarshal(b, &msg); err != nil {
		log.Panicf("Could not json.Unmarshal() a (supposed) RobustMessage (%v): %v\n", b, err)
	}
	return msg
}
