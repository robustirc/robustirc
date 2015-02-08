// Package ircserver implements an IRC server which strictly adheres to a
// processing model where output is only ever generated in response to input,
// and only depends on state that is local to the IRC server.
//
// That means the output of two IRC server instances will be byte-for-byte
// identical if you feed them exactly the same input (in the same order), which
// is what RobustIRC does when recovering from a raft snapshot.
package ircserver

import (
	"errors"
	"fmt"
	"log"
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

	// CursorEOF is returned by a logCursor when there are no more messages.
	CursorEOF = errors.New("No more messages")
)

var (
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
)

func init() {
	prometheus.MustRegister(messagesProcessed)
}

// logCursor is like a database cursor: it returns the next message.
// ircCommand’s StillRelevant callback will get a cursor for all messages
// _before_ the current message and a separate cursor returning all
// messages _after_ the current message.
type logCursor func() (*irc.Message, error)

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

// InterestedIn returns true if the session should receive the specified 'msg'.
// As an example, InterestedIn returns true for PRIVMSGs if the session is in
// the channel to which the PRIVMSG was sent.
func (s *Session) InterestedIn(serverPrefix *irc.Prefix, msg *types.RobustMessage) bool {
	if msg.Type == types.RobustPing {
		return true
	}
	ircmsg := irc.ParseMessage(msg.Data)

	// Everything the server sends directly is interesting to the client.
	if *ircmsg.Prefix == *serverPrefix && msg.Session == s.Id {
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

type IRCServer struct {
	// sessions contains all sessions, i.e. nickname, away message, whether the
	// session is an IRC operator, etc. In contrast to nicks, this is keyed by
	// the session id.
	sessions map[types.RobustId]*Session

	// nicks maps from nicknames in lower-case (e.g. NickToLower("sECuRE")) to
	// session pointers. Being able to quickly look up sessions based on their
	// nickname is handy to implement IRC commands efficiently.
	nicks map[string]*Session

	// channels is a map containing the properties of every known channel (e.g.
	// topic or modes), keyed by the channel name (e.g. “#robustirc”).
	channels map[string]*channel

	// output is filled in SendMessages with messages that were generated by
	// ProcessMessage. These messages are not specific to any IRC client; the
	// InterestedIn function is used to figure out which IRC client(s) are
	// interested in that message.
	output *outputstream.OutputStream

	// ServerPrefix is the prefix for output messages that come from the
	// server, as opposed to from a client.
	ServerPrefix *irc.Prefix

	// lastProcessed is the id of the last message fed into ProcessMessage.
	lastProcessed types.RobustId

	// serverCreation is the time at which the IRCServer object was created.
	// Used for the RPL_CREATED message.
	serverCreation time.Time
}

// NewIRCServer returns a new IRC server.
func NewIRCServer(networkname string) *IRCServer {
	return &IRCServer{
		channels:       make(map[string]*channel),
		nicks:          make(map[string]*Session),
		sessions:       make(map[types.RobustId]*Session),
		output:         outputstream.NewOutputStream(),
		ServerPrefix:   &irc.Prefix{Name: networkname},
		serverCreation: time.Now(),
	}
}

// UpdateLastMessage stores the clientmessageid of the last message in the
// corresponding session, so that duplicate messages are not persisted twice.
func (i *IRCServer) UpdateLastClientMessageID(msg *types.RobustMessage, serialized []byte) error {
	session, err := i.GetSession(msg.Session)
	if err != nil {
		return err
	}
	session.LastActivity = time.Unix(0, msg.Id.Id)
	session.LastClientMessageId = msg.ClientMessageId
	session.LastPostMessageReply = serialized
	return nil
}

// CreateSession creates a new session (equivalent to an IRC connection).
func (i *IRCServer) CreateSession(id types.RobustId, auth string) {
	i.sessions[id] = &Session{
		Id:           id,
		Auth:         auth,
		StartId:      i.output.LastSeen(),
		Channels:     make(map[string]bool),
		LastActivity: time.Unix(0, id.Id),
	}
}

// DeleteSession deletes the specified session. Called from the IRC server
// itself (when processing QUIT or KILL) or from the API (DELETE request coming
// from the bridge).
func (i *IRCServer) DeleteSession(id types.RobustId) {
	delete(i.sessions, id)
}

// ExpireSessions returns RobustDeleteSession RobustMessages for all sessions
// that are older than timeout. These messages are then applied to raft.
func (i *IRCServer) ExpireSessions(timeout time.Duration) []*types.RobustMessage {
	var deletes []*types.RobustMessage

	for id, s := range i.sessions {
		if time.Since(s.LastActivity) <= timeout {
			continue
		}

		log.Printf("Expiring session %v\n", id)

		deletes = append(deletes, types.NewRobustMessage(
			types.RobustDeleteSession, id, fmt.Sprintf("Ping timeout (%v)", timeout)))
	}
	return deletes
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
// more IRC messages in response to 'message'. These messages can then be
// stored for eventual retrieval by the clients by calling SendMessages.
func (i *IRCServer) ProcessMessage(session types.RobustId, message *irc.Message) []*irc.Message {
	// alias for convenience
	s := i.sessions[session]

	messagesProcessed.WithLabelValues(message.Command).Inc()

	if !s.loggedIn() && message.Command != irc.NICK && message.Command != irc.USER {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTREGISTERED,
			Params:   []string{message.Command},
			Trailing: "You have not registered",
		}}
	}

	cmd, ok := commands[strings.ToUpper(message.Command)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_UNKNOWNCOMMAND,
			Params:   []string{s.Nick, message.Command},
			Trailing: "Unknown command",
		}}
	}

	if len(message.Params) < cmd.MinParams {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, message.Command},
			Trailing: "Not enough parameters",
		}}
	}

	replies := cmd.Func(i, s, message)
	for _, reply := range replies {
		if reply.Prefix == nil {
			reply.Prefix = i.ServerPrefix
		}
	}
	return replies
}

// SendMessages appends the specified batch of messages to the output, marking
// them as a response to the incoming message with id 'id' and associating them
// with session 'session'. IRC clients will eventually receive these messages
// by calling GetNext.
func (i *IRCServer) SendMessages(replies []*irc.Message, session types.RobustId, id int64) {
	i.lastProcessed = types.RobustId{Id: id}

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

	i.output.Add(robustreplies)
}

// SendPing appends a ping message to the output, including the current servers
// in the network so that bridges don’t need to re-resolve the DNS record and
// can still get a more recent server list.
func (i *IRCServer) SendPing(master net.Addr, peers []net.Addr) {
	pingmsg := types.NewRobustMessage(types.RobustPing, types.RobustId{}, "")
	for _, peer := range peers {
		pingmsg.Servers = append(pingmsg.Servers, peer.String())
	}
	if master != nil {
		pingmsg.Currentmaster = master.String()
	}
	i.output.Add([]*types.RobustMessage{pingmsg})
}

// GetNextNonBlocking is used for the irclog handler and is equivalent to
// GetNext, except that it doesn’t block until new messages are available when
// there are no more messages to get.
func (i *IRCServer) GetNextNonBlocking(lastseen types.RobustId) *types.RobustMessage {
	// TODO(secure): fix
	return nil
}

// GetSession returns a pointer to the session specified by 'id'.
//
// It returns ErrNoSuchSession when the session definitely does not exist
// (based on the timestamp), but ErrSessionNotYetSeen when the session might
// exist in the future, but was not yet seen on this IRC instance server
// (perhaps due to raft delay).
func (i *IRCServer) GetSession(id types.RobustId) (*Session, error) {
	s, ok := i.sessions[id]
	if ok {
		return s, nil
	}

	// TODO(secure): think about and document implications of timestamp drift
	if time.Unix(0, i.lastProcessed.Id).Sub(time.Unix(0, id.Id)) > 0 {
		// We processed a newer message than that session identifier, so
		// the session definitely does not exist.
		return nil, ErrNoSuchSession
	} else {
		return nil, ErrSessionNotYetSeen
	}
}

// StillRelevant returns true if and only if ircmsg is still relevant, i.e.
// needs to be kept in the IRC server input in order to arrive at exactly the
// same state when replaying the input. State refers to ircserver state except
// for outputstream.
//
// As an example, PRIVMSGs are never relevant, as they never modify ircserver
// state. Also, if there are two NICK messages in the same session directly one
// after the other, the first NICK message is obsoleted by the following NICK
// message, i.e. not relevant anymore. Once there are other messages in between
// the two NICK messages, though, it is not that simple anymore. Refer to
// relevantNick() for how it works.
//
// 'prev' and 'next' are cursors with which the message-specific handler can
// look at other messages to figure out whether the message is still relevant.
func (i *IRCServer) StillRelevant(session *Session, ircmsg *irc.Message, prev, next logCursor) (bool, error) {
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

// GetSessions returns a copy of sessions that can be used in the status
// handler (i.e. it goes stale, but doesn’t block other operations).
func (i *IRCServer) GetSessions() map[types.RobustId]Session {
	result := make(map[types.RobustId]Session, len(i.sessions))
	for id, session := range i.sessions {
		result[id] = *session
	}
	return result
}

// NumSessions returns the current number of sessions.
func (i *IRCServer) NumSessions() int {
	return len(i.sessions)
}

// GetNext wraps outputstream.GetNext to avoid making the outputstream instance
// public. All other methods should not be used, which is enforced this way.
func (i *IRCServer) GetNext(lastseen types.RobustId) []*types.RobustMessage {
	return i.output.GetNext(lastseen)
}
