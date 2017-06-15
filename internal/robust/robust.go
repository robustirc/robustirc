package robust

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"gopkg.in/sorcix/irc.v2"

	pb "github.com/robustirc/robustirc/internal/proto"
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
	Id    uint64
	Reply uint64
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

	// UnixNano is set by the server which accepts the message.
	UnixNano int64

	// InterestingFor is a map from session ids (only the Id part of a
	// robust.Id, since Reply is always unset for sessions) to a bool
	// that signals whether the session is interested in the message.
	// InterestingFor gets set once in SendMessages and stays
	// constant.
	InterestingFor map[uint64]bool `json:"-"`

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

	// RemoteAddr is the network address that sent the request.
	RemoteAddr string `json:",omitempty"`
}

func (m *Message) Timestamp() time.Time {
	// For messages which were created before we introduced UnixNano:
	if m.UnixNano == 0 {
		return time.Unix(0, int64(m.Id.Id))
	}
	return time.Unix(0, m.UnixNano)
}

func (m *Message) TimestampString() string {
	return m.Timestamp().Format("2006-01-02 15:04:05 -07:00")
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

func (m *Message) ProtoMessage() *pb.RobustMessage {
	return &pb.RobustMessage{
		Id: &pb.RobustId{
			Id:    m.Id.Id,
			Reply: m.Id.Reply,
		},
		Session: &pb.RobustId{
			Id:    m.Session.Id,
			Reply: m.Session.Reply,
		},
		Type:            pb.RobustMessage_RobustType(m.Type),
		Data:            m.Data,
		UnixNano:        m.UnixNano,
		Servers:         m.Servers,
		CurrentMaster:   m.Currentmaster,
		ClientMessageId: m.ClientMessageId,
		Revision:        m.Revision,
		RemoteAddr:      m.RemoteAddr,
	}
}

// CopyToProtoMessage writes the message to dst, assuming that dst is a fully
// allocated RobustMessage.
func (m *Message) CopyToProtoMessage(dst *pb.RobustMessage) {
	dst.Id.Id = m.Id.Id
	dst.Id.Reply = m.Id.Reply
	dst.Session.Id = m.Session.Id
	dst.Session.Reply = m.Session.Reply
	dst.Type = pb.RobustMessage_RobustType(m.Type)
	dst.Data = m.Data
	dst.UnixNano = m.UnixNano
	dst.Servers = m.Servers
	dst.CurrentMaster = m.Currentmaster
	dst.ClientMessageId = m.ClientMessageId
	dst.Revision = m.Revision
	dst.RemoteAddr = m.RemoteAddr
}

func NewMessageFromBytes(b []byte, index uint64) Message {
	var msg Message
	if len(b) > 0 && b[0] == 'p' {
		var p pb.RobustMessage
		if err := proto.Unmarshal(b[1:], &p); err != nil {
			log.Panicf("Could not proto.Unmarshal() a (supposed) robust.Message (%x): %v", b[1:], err)
		}
		msg.Id.Id = p.Id.Id
		msg.Id.Reply = p.Id.Reply
		msg.Session.Id = p.Session.Id
		msg.Session.Reply = p.Session.Reply
		msg.Type = Type(p.Type)
		msg.Data = p.Data
		msg.UnixNano = p.UnixNano
		msg.Servers = p.Servers
		msg.Currentmaster = p.CurrentMaster
		msg.ClientMessageId = p.ClientMessageId
		msg.Revision = p.Revision
		msg.RemoteAddr = p.RemoteAddr
	} else {
		if err := json.Unmarshal(b, &msg); err != nil {
			log.Panicf("Could not json.Unmarshal() a (supposed) robust.Message (%v): %v\n", b, err)
		}
	}
	if msg.Id.Id == 0 {
		msg.Id.Id = index
	}
	return msg
}
