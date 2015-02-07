// ircserver is the entry point for all IRC-related logic.
package ircserver

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/robustirc/outputstream"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
)

const (
	maxNickLen    = "30"
	maxChannelLen = "32"

	// Message format according to RFC2812, section 2.3.1
	// A-Z / a-z
	letter = `\x41-\x5A\x61-\x7A`
	// 0-9
	digit = `\x30-\x39`
	// "[", "]", "\", "`", "_", "^", "{", "|", "}"
	special = `\x5B-\x60\x7B-\x7D`

	// any octet except NUL, BELL, CR, LF, " ", "," and ":"
	chanstring = `\x01-\x06\x08-\x09\x0B-\x0C\x0E-\x1F\x21-\x2B\x2D-\x39\x3B-\xFF`
)

var (
	validNickRe    = regexp.MustCompile(`^[` + letter + special + `][` + letter + digit + special + `-]{0,` + maxNickLen + `}$`)
	validChannelRe = regexp.MustCompile(`^#[` + chanstring + `]{0,` + maxChannelLen + `}$`)

	// The session was not (yet?) seen on this follower. We cannot say with
	// confidence that it does not exist.
	ErrSessionNotYetSeen = errors.New("Session not yet seen")

	// The session definitely does not exist.
	ErrNoSuchSession = errors.New("No such session")
)

var (
	serverCreation = time.Now()
	Sessions       = make(map[types.RobustId]*Session)
	channels       map[string]*channel
	nicks          map[string]*Session
	ServerPrefix   *irc.Prefix

	lastProcessed types.RobustId

	commands = make(map[string]*ircCommand)

	// TODO(secure): remove this once OPER uses custom (configured) passwords.
	NetworkPassword string

	messagesProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "irc",
			Name:      "messages_processed",
			Help:      "Number of messages processed by message command",
		},
		[]string{"command"},
	)

	sessionsGauge = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Subsystem: "irc",
			Name:      "sessions",
			Help:      "Number of IRC sessions",
		},
		func() float64 {
			return float64(len(Sessions))
		},
	)
)

func init() {
	prometheus.MustRegister(messagesProcessed)
	prometheus.MustRegister(sessionsGauge)
}

// logCursor is like a database cursor: it returns the next message.
// ircCommand’s StillRelevant callback will get a cursor for all messages
// _before_ the current message and a separate cursor returning all
// messages _after_ the current message.
type logCursor func() (*irc.Message, error)

var CursorEOF = errors.New("No more messages")

type ircCommand struct {
	Func func(*Session, *irc.Message) []*irc.Message

	// Interesting returns true if the message should be sent to the session.
	Interesting func(*Session, *irc.Message) bool

	// StillRelevant is used during compaction. If it returns true, the message
	// is kept, otherwise it will be deleted.
	StillRelevant func(*Session, *irc.Message, logCursor, logCursor) (bool, error)

	// MinParams ensures that enough parameters were specified.
	// irc.ERR_NEEDMOREPARAMS is returned in case less than MinParams
	// parameters were found, otherwise, Func is called.
	MinParams int
}

type Session struct {
	Id           types.RobustId
	Auth         string
	Nick         string
	Username     string
	Realname     string
	Channels     map[string]bool
	LastActivity time.Time
	Operator     bool
	AwayMsg      string

	// The current IRC message id at the time when the session was started.
	// This is used in handleGetMessages to skip uninteresting messages.
	StartId types.RobustId

	// The last ClientMessageId we got.
	LastClientMessageId  uint64
	LastPostMessageReply []byte

	ircPrefix irc.Prefix
}

func (s *Session) loggedIn() bool {
	return s.Nick != "" && s.Username != ""
}

// updateIrcPrefix MUST be called whenever the Nick field changes.
func (s *Session) updateIrcPrefix() {
	s.ircPrefix = irc.Prefix{
		Name: s.Nick,
		User: s.Username,
		// Similar to FreeNode’s “unaffiliated/foo”, so clients should already
		// support this format.
		Host: fmt.Sprintf("robust/0x%x", s.Id.Id),
	}
}

func (s *Session) InterestedIn(msg *types.RobustMessage) bool {
	if msg.Type == types.RobustPing {
		return true
	}
	ircmsg := irc.ParseMessage(msg.Data)

	// Everything the server sends directly is interesting to the client.
	if *ircmsg.Prefix == *ServerPrefix && msg.Session == s.Id {
		return true
	}

	// No strings.ToUpper(ircmsg.Command) because we generate all messages,
	// thus they are well-formed.
	cmd, ok := commands[ircmsg.Command]
	if !ok || cmd.Interesting == nil {
		return false
	}

	return cmd.Interesting(s, ircmsg)
}

const (
	chanop = iota
	voice
	maxChanMemberStatus
)

type channel struct {
	topicNick string
	topicTime time.Time
	topic     string

	nicks map[string]*[maxChanMemberStatus]bool

	// We waste 65 bytes per channel for clearer code (being able to directly
	// access modes by using their letter as an index).
	modes ['z']bool
}

func ClearState() {
	channels = make(map[string]*channel)
	nicks = make(map[string]*Session)
	Sessions = make(map[types.RobustId]*Session)
	outputstream.Reset()
}

// UpdateLastMessage stores the clientmessageid of the last message in the
// corresponding session, so that duplicate messages are not persisted twice.
func UpdateLastMessage(msg *types.RobustMessage, serialized []byte) error {
	session, err := GetSession(msg.Session)
	if err != nil {
		return err
	}
	session.LastActivity = time.Unix(0, msg.Id.Id)
	session.LastClientMessageId = msg.ClientMessageId
	session.LastPostMessageReply = serialized
	return nil
}

// CreateSession creates a new session (equivalent to an IRC connection).
func CreateSession(id types.RobustId, auth string) {
	Sessions[id] = &Session{
		Id:           id,
		Auth:         auth,
		StartId:      outputstream.LastSeen(),
		Channels:     make(map[string]bool),
		LastActivity: time.Unix(0, id.Id),
	}
}

func DeleteSession(id types.RobustId) {
	delete(Sessions, id)
}

// IsValidNickname returns true if the provided nickname is valid according to
// RFC2812 (see https://tools.ietf.org/html/rfc2812#section-2.3.1), otherwise
// false.
func IsValidNickname(nick string) bool {
	return validNickRe.MatchString(nick)
}

func IsValidChannel(channel string) bool {
	return validChannelRe.MatchString(channel)
}

// NickToLower converts a nickname to lower case, following RFC2812:
//
// Because of IRC's scandanavian origin, the characters {}| are
// considered to be the lower case equivalents of the characters []\,
// respectively. This is a critical issue when determining the
// equivalence of two nicknames.
func NickToLower(nick string) string {
	r := strings.NewReplacer("[", "{", "]", "}", "\\", "|")
	return r.Replace(strings.ToLower(nick))
}

// ProcessMessage modifies state in response to 'message' and returns zero or
// more IRC messages in response to 'message'.
func ProcessMessage(session types.RobustId, message *irc.Message) []*irc.Message {
	// alias for convenience
	s := Sessions[session]

	messagesProcessed.WithLabelValues(message.Command).Inc()

	if !s.loggedIn() && message.Command != irc.NICK && message.Command != irc.USER {
		return []*irc.Message{&irc.Message{
			Prefix:   ServerPrefix,
			Command:  irc.ERR_NOTREGISTERED,
			Params:   []string{message.Command},
			Trailing: "You have not registered",
		}}
	}

	cmd, ok := commands[strings.ToUpper(message.Command)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Prefix:   ServerPrefix,
			Command:  irc.ERR_UNKNOWNCOMMAND,
			Params:   []string{s.Nick, message.Command},
			Trailing: "Unknown command",
		}}
	}

	if len(message.Params) < cmd.MinParams {
		return []*irc.Message{&irc.Message{
			Prefix:   ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, message.Command},
			Trailing: "Not enough parameters",
		}}
	}

	replies := cmd.Func(s, message)
	for _, reply := range replies {
		if reply.Prefix == nil {
			reply.Prefix = ServerPrefix
		}
	}
	return replies
}

func SendMessages(replies []*irc.Message, session types.RobustId, id int64) {
	lastProcessed = types.RobustId{Id: id}

	if len(replies) == 0 {
		return
	}

	robustreplies := make([]*types.RobustMessage, len(replies))
	for idx, reply := range replies {
		robustmsg := types.NewRobustMessage(types.RobustIRCToClient, session, string(reply.Bytes()))
		// The IDs must be the same across servers.
		robustmsg.Id = types.RobustId{
			Id:    id,
			Reply: int64(idx + 1),
		}
		robustreplies[idx] = robustmsg
	}

	outputstream.Add(robustreplies)
}

func SendPing(master net.Addr, peers []net.Addr) {
	pingmsg := types.NewRobustMessage(types.RobustPing, types.RobustId{}, "")
	for _, peer := range peers {
		pingmsg.Servers = append(pingmsg.Servers, peer.String())
	}
	if master != nil {
		pingmsg.Currentmaster = master.String()
	}
	outputstream.Add([]*types.RobustMessage{pingmsg})
}

func GetMessageNonBlocking(lastseen types.RobustId) *types.RobustMessage {
	// TODO(secure): fix
	return nil
}

func GetSession(id types.RobustId) (*Session, error) {
	s, ok := Sessions[id]
	if ok {
		return s, nil
	}

	if time.Unix(0, lastProcessed.Id).Sub(time.Unix(0, id.Id)) > 0 {
		// We processed a newer message than that session identifier, so
		// the session definitely does not exist.
		return nil, ErrNoSuchSession
	} else {
		return nil, ErrSessionNotYetSeen
	}
}

func StillRelevant(session *Session, ircmsg *irc.Message, prev, next logCursor) (bool, error) {
	if ircmsg == nil {
		return true, nil
	}

	c, ok := commands[strings.ToUpper(ircmsg.Command)]
	if !ok {
		return true, nil
	}

	// If unable to figure out whether the message is still relevant, keep it.
	if c.StillRelevant == nil {
		return true, nil
	}

	return c.StillRelevant(session, ircmsg, prev, next)
}
