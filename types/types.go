// Package types implements some types common to the ircserver and robustirc packages.
package types

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/sorcix/irc"
)

// RobustId is a message id it the RobustIRC protocol.
type RobustId struct {
	Id    int64
	Reply int64
}

// String implements the fmt.Stringer interface.
func (i *RobustId) String() string {
	return fmt.Sprintf("%d.%d", i.Id, i.Reply)
}

// RobustType is the type of a RobustMessage
type RobustType int64

const (
	// RobustCreateSession represents a POST /robustirc/v1/session request.
	RobustCreateSession = iota
	// RobustDeleteSession represents a DELETE /robustirc/v1/$sessionid
	// request.
	RobustDeleteSession
	// RobustIRCFromClient represents a POST /robustirc/v1/$sessionid/message
	// request.
	RobustIRCFromClient
	// RobustIRCToClient represents a GET
	// /robustirc/v1/$sessionid/messages?lastseen=$msgid response.
	RobustIRCToClient
	// RobustPing represents a keepalive ping from the server to a set of clients.
	RobustPing
	// RobustMessageOfDeath represents a special message type for messages,
	// that killed an IRC server and thus should not be applied to any server
	// in the cluster.
	RobustMessageOfDeath
)

// RobustMessage is a message, that is replicated among the raft cluster and
// represents an event in the lifecycle of the ircserver. It is also the type
// that is marshalled and sent to api-clients.
type RobustMessage struct {
	// Id is the msgid of this message.
	Id RobustId
	// Session is the id of the session, that created this message.
	Session RobustId
	// Type is the type of the message.
	Type RobustType
	// Data contains the payload of the message.
	Data string

	// Servers lists of all servers currently in the network. Only present when
	// Type == RobustPing.
	Servers []string `json:",omitempty"`

	// CurrentMaster is the current leader of the raft cluster, as a hint for
	// the proxy (may save one redirect).
	Currentmaster string `json:",omitempty"`

	// ClientMessageId sent by client. Only present when Type == RobustIRCFromClient.
	ClientMessageId uint64 `json:",omitempty"`
}

// Timestamp returns the unix timestamp, this message was created, as a string.
func (m *RobustMessage) Timestamp() string {
	return time.Unix(0, m.Id.Id).Format("2006-01-02 15:04:05 -07:00")
}

// PrivacyFilter checks, if the message is a PRIVMSG, and if so, filters the
// actaul content, so that it does not appear in log-output.
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

// NewRobustMessage creates a new RobustMessage, for the given session, with
// the given type and data.
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

// NewRobustMessageFromBytes parses a serialized RobustMessage from the given
// byte slice and returns it.
func NewRobustMessageFromBytes(b []byte) RobustMessage {
	var msg RobustMessage
	if err := json.Unmarshal(b, &msg); err != nil {
		log.Fatalf("Could not json.Unmarshal() a (supposed) RobustMessage (%v): %v\n", b, err)
	}
	return msg
}

// TODO(mero): Put serialization also into this package.

// TODO(mero): Should NewRobustMessageFromBytes really return a value, not a
// pointer? Seems inconsistent with NewRobustMessage.

// TODO(mero): golint says, we should spell Id as ID. Do this, once we caught
// up with the backlog in code-reviews, to avoid merge-conflits. See also:
// https://code.google.com/p/go-wiki/wiki/CodeReviewComments#Initialisms
