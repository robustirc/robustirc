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
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/robustirc/config"
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
type logCursor func(wantType types.RobustType) (*types.RobustMessage, error)

// lcChan is a lower-case channel name, e.g. “#chaos-hd”, even when the user
// sent “JOIN #Chaos-HD”. It is used to enforce using ChanToLower() on keys of
// various maps.
type lcChan string

// lcNick is a lower-case nickname, e.g. “secure”, even when the user sent
// “NICK sECuRE”. It is used to enforce using NickToLower() on keys of various
// maps.
type lcNick string

type Session struct {
	Id           types.RobustId
	auth         string
	Nick         string
	Username     string
	Realname     string
	Channels     map[lcChan]bool
	LastActivity time.Time
	Operator     bool
	AwayMsg      string

	// throttlingExponent starts at 0 and is increased on every subsequent
	// message until 2^throttlingExponent ≥ netConfig.PostMessageCooloff.
	// It will be reset once the user does not send messages for
	// netConfig.PostMessageCooloff.
	throttlingExponent int

	invitedTo map[lcChan]bool

	// We waste 65 bytes per session for clearer code (being able to directly
	// access modes by using their letter as an index).
	modes ['z']bool

	// svid is an identifier set by the services. It starts out as 0 and gets
	// set to something >0 once the nickname identified itself.
	svid string

	// The (raw) password from a PASS command.
	Pass string

	Server bool

	// The current IRC message id at the time when the session was started.
	// This is used in handleGetMessages to skip uninteresting messages.
	startId types.RobustId

	// The last ClientMessageId we got.
	lastClientMessageId  uint64
	lastPostMessageReply []byte

	ircPrefix irc.Prefix
	// deleted gets set by DeleteSession and used by SendMessages. Refer to the
	// DeleteSession comment.
	deleted bool
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

const (
	chanop = iota
	voice
	maxChanMemberStatus
)

type channel struct {
	// name is the (case-sensitive!) original name this channel had when it was
	// first created.
	name string

	topicNick string
	topicTime time.Time
	topic     string

	nicks map[lcNick]*[maxChanMemberStatus]bool

	// We waste 65 bytes per channel for clearer code (being able to directly
	// access modes by using their letter as an index).
	modes ['z']bool
}

type IRCServer struct {
	// sessions contains all sessions, i.e. nickname, away message, whether the
	// session is an IRC operator, etc. In contrast to nicks, this is keyed by
	// the session id.
	sessions   map[types.RobustId]*Session
	sessionsMu *sync.RWMutex

	// serverSessions is a slice that contains the IDs of all sessions that
	// represent server-to-server connections, so that they can efficiently be
	// added in e.g. interestJoin.
	serverSessions []int64

	// nicks maps from nicknames in lower-case (e.g. NickToLower("sECuRE")) to
	// session pointers. Being able to quickly look up sessions based on their
	// nickname is handy to implement IRC commands efficiently.
	nicks map[lcNick]*Session

	// channels is a map containing the properties of every known channel (e.g.
	// topic or modes), keyed by the lower-case channel name (e.g.
	// ChanToLower(“#robustirc”)).
	channels map[lcChan]*channel

	// output is filled in SendMessages with messages that were generated by
	// ProcessMessage. These messages are not specific to any IRC client; the
	// InterestedIn function is used to figure out which IRC client(s) are
	// interested in that message.
	output *outputstream.OutputStream

	// ServerPrefix is the prefix for output messages that come from the
	// server, as opposed to from a client.
	ServerPrefix *irc.Prefix

	// lastProcessed is the id of the last message fed into ProcessMessage.
	lastProcessed   types.RobustId
	lastProcessedMu *sync.RWMutex

	// serverCreation is the time at which the IRCServer object was created.
	// Used for the RPL_CREATED message.
	ServerCreation time.Time

	// Config contains the IRC-related part of the RobustIRC configuration.
	Config config.IRC
}

// NewIRCServer returns a new IRC server.
func NewIRCServer(networkname string, serverCreation time.Time) *IRCServer {
	return &IRCServer{
		channels:        make(map[lcChan]*channel),
		nicks:           make(map[lcNick]*Session),
		sessions:        make(map[types.RobustId]*Session),
		sessionsMu:      &sync.RWMutex{},
		lastProcessedMu: &sync.RWMutex{},
		output:          outputstream.NewOutputStream(),
		ServerPrefix:    &irc.Prefix{Name: networkname},
		ServerCreation:  serverCreation,
	}
}

// NewRobustMessage creates a new RobustMessage with an id that is guaranteed
// to be higher than the last processed message (it panics otherwise) so that
// timestamp drift is loudly complained about instead of silently accepted to
// the point where it breaks the network’s regular operation.
func (i *IRCServer) NewRobustMessage(t types.RobustType, session types.RobustId, data string) *types.RobustMessage {
	unixnano := time.Now().UnixNano()
	i.lastProcessedMu.RLock()
	defer i.lastProcessedMu.RUnlock()
	if unixnano < i.lastProcessed.Id {
		panic(fmt.Sprintf("Assumption violated: current time %d is older than the timestamp of the last processed message (%d)", unixnano, i.lastProcessed.Id))
	}
	return &types.RobustMessage{
		Id:      types.RobustId{Id: unixnano},
		Session: session,
		Type:    t,
		Data:    data,
	}
}

// UpdateLastMessage stores the clientmessageid of the last message in the
// corresponding session, so that duplicate messages are not persisted twice.
func (i *IRCServer) UpdateLastClientMessageID(msg *types.RobustMessage, serialized []byte) error {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	session, err := i.GetSession(msg.Session)
	if err != nil {
		return err
	}
	session.LastActivity = time.Unix(0, msg.Id.Id)
	session.lastClientMessageId = msg.ClientMessageId
	session.lastPostMessageReply = serialized
	return nil
}

// CreateSession creates a new session (equivalent to an IRC connection).
func (i *IRCServer) CreateSession(id types.RobustId, auth string) {
	i.sessions[id] = &Session{
		Id:           id,
		auth:         auth,
		startId:      i.output.LastSeen(),
		Channels:     make(map[lcChan]bool),
		invitedTo:    make(map[lcChan]bool),
		LastActivity: time.Unix(0, id.Id),
		svid:         "0",
	}
}

// DeleteSessionById calls DeleteSession, but must be used when calling from
// outside the IRC Server (it acquires a lock for you).
func (i *IRCServer) DeleteSessionById(sessionid types.RobustId) {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	session, ok := i.sessions[sessionid]
	if ok {
		i.DeleteSession(session)
	}
}

// DeleteSession deletes the specified session. Called from the IRC server
// itself (when processing QUIT or KILL) or from the API (DELETE request coming
// from the bridge).
func (i *IRCServer) DeleteSession(s *Session) {
	for _, c := range i.channels {
		delete(c.nicks, NickToLower(s.Nick))

		i.maybeDeleteChannel(c)
	}
	delete(i.nicks, NickToLower(s.Nick))
	// Instead of deleting the session here, we defer that to SendMessages, as
	// SendMessages calls the Interesting function of each reply (such as a
	// QUIT reply) and that function might still need access to the session to
	// determine where the reply should be sent to.
	s.deleted = true
}

// ExpireSessions returns RobustDeleteSession RobustMessages for all sessions
// that are older than timeout. These messages are then applied to raft.
func (i *IRCServer) ExpireSessions(timeout time.Duration) []*types.RobustMessage {
	var deletes []*types.RobustMessage

	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	for id, s := range i.sessions {
		// Skip sessions that were introduced by services (e.g. NickServ).
		if id.Reply != 0 {
			continue
		}
		if time.Since(s.LastActivity) <= timeout {
			continue
		}

		log.Printf("Expiring session %v\n", id)

		deletes = append(deletes, i.NewRobustMessage(
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

func IsServicesNickname(nick string) bool {
	return strings.HasSuffix(strings.ToLower(nick), "serv")
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
func NickToLower(nick string) lcNick {
	r := strings.NewReplacer("[", "{", "]", "}", "\\", "|")
	return lcNick(r.Replace(strings.ToLower(nick)))
}

// ChanToLower converts a channel to lower case.
func ChanToLower(channelname string) lcChan {
	return lcChan(strings.ToLower(channelname))
}

func extractPassword(password, prefix string) string {
	var extracted string
	for _, part := range strings.Split(password, ":") {
		if strings.HasPrefix(strings.ToLower(part), prefix+"=") {
			extracted = part[len(prefix+"="):]
		}

		// Append prefix-less strings to the extracted password, chances are
		// the user used a colon in the password.
		if !strings.HasPrefix(part, "nickserv=") &&
			!strings.HasPrefix(part, "services=") &&
			!strings.HasPrefix(part, "network=") &&
			!strings.HasPrefix(part, "session=") &&
			!strings.HasPrefix(part, "oper=") &&
			extracted != "" {
			extracted = extracted + ":" + part
		}
	}
	return extracted
}

func (i *IRCServer) maybeDeleteChannel(c *channel) {
	if len(c.nicks) > 0 {
		return
	}
	lc := ChanToLower(c.name)
	delete(i.channels, lc)
	for _, s := range i.sessions {
		delete(s.invitedTo, lc)
	}
}

// ProcessMessage modifies state in response to 'message' and returns zero or
// more IRC messages in response to 'message'. These messages can then be
// stored for eventual retrieval by the clients by calling SendMessages.
func (i *IRCServer) ProcessMessage(session types.RobustId, message *irc.Message) []*irc.Message {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	// alias for convenience
	s := i.sessions[session]
	command := strings.ToUpper(message.Command)

	messagesProcessed.WithLabelValues(command).Inc()

	if !s.loggedIn() && !s.Server &&
		command != irc.NICK &&
		command != irc.USER &&
		command != irc.PASS &&
		command != irc.QUIT &&
		command != irc.SERVER {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTREGISTERED,
			Params:   []string{command},
			Trailing: "You have not registered",
		}}
	}

	var serverPrefix string
	if s.Server {
		serverPrefix = "server_"
	}
	cmd, ok := commands[serverPrefix+command]
	if !ok {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_UNKNOWNCOMMAND,
			Params:   []string{s.Nick, command},
			Trailing: "Unknown command",
		}}
	}

	if len(message.Params) < cmd.MinParams {
		return []*irc.Message{&irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, command},
			Trailing: "Not enough parameters",
		}}
	}

	replies := cmd.Func(i, s, message)
	for _, reply := range replies {
		if reply.Prefix == nil {
			reply.Prefix = i.ServerPrefix
		} else if reply.Prefix.Name == "" {
			reply.Prefix = nil
		}
	}
	return replies
}

// SendMessages appends the specified batch of messages to the output, marking
// them as a response to the incoming message with id 'id' and associating them
// with session 'session'. IRC clients will eventually receive these messages
// by calling GetNext.
func (i *IRCServer) SendMessages(replies []*irc.Message, session types.RobustId, id int64) {
	i.lastProcessedMu.Lock()
	i.lastProcessed = types.RobustId{Id: id}
	i.lastProcessedMu.Unlock()

	defer func() {
		i.sessionsMu.Lock()
		defer i.sessionsMu.Unlock()
		if s, ok := i.sessions[session]; ok {
			if s.Server {
				for id, session := range i.sessions {
					if id.Id == s.Id.Id && id.Reply != 0 && session.deleted {
						delete(i.sessions, id)
					}
				}
			}
			if s.deleted {
				delete(i.sessions, session)
			}
		}
	}()

	if len(replies) == 0 {
		return
	}

	robustreplies := make([]*types.RobustMessage, len(replies))
	for idx, reply := range replies {
		robustmsg := &types.RobustMessage{
			// The IDs must be the same across servers.
			Id: types.RobustId{
				Id:    id,
				Reply: int64(idx + 1),
			},
			Session: session,
			Type:    types.RobustIRCToClient,
			Data:    string(reply.Bytes()),
		}

		if reply.Prefix == nil {
			server := false
			i.sessionsMu.RLock()
			if s, ok := i.sessions[session]; ok {
				server = s.Server
			}
			i.sessionsMu.RUnlock()
			if server {
				robustmsg.InterestingFor = make(map[int64]bool)
				robustmsg.InterestingFor[session.Id] = true
				for _, serverid := range i.serverSessions {
					robustmsg.InterestingFor[serverid] = true
				}
				robustreplies[idx] = robustmsg
				continue
			}
		}

		// No strings.ToUpper(ircmsg.Command) because we generate all messages,
		// thus they are well-formed.
		cmd, ok := commands[reply.Command]
		if ok && cmd.Interesting != nil {
			robustmsg.InterestingFor = cmd.Interesting(i, session, reply)
			robustreplies[idx] = robustmsg
			continue
		}

		// Everything the server sends directly is interesting to the client.
		if reply.Prefix != nil && *reply.Prefix == *i.ServerPrefix {
			robustmsg.InterestingFor = make(map[int64]bool)
			robustmsg.InterestingFor[session.Id] = true
			robustreplies[idx] = robustmsg
			continue
		}

		robustmsg.InterestingFor = make(map[int64]bool)
		robustreplies[idx] = robustmsg
	}

	i.output.Add(robustreplies)
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
	i.lastProcessedMu.RLock()
	defer i.lastProcessedMu.RUnlock()
	if time.Unix(0, i.lastProcessed.Id).Sub(time.Unix(0, id.Id)) > 0 {
		// We processed a newer message than that session identifier, so
		// the session definitely does not exist.
		return nil, ErrNoSuchSession
	} else {
		return nil, ErrSessionNotYetSeen
	}
}

// GetAuth returns the authentication string of |sessionid|, a random shared
// secret between the RobustIRC network and the client to prevent session
// hijacking.
func (i *IRCServer) GetAuth(sessionid types.RobustId) (string, error) {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	session, err := i.GetSession(sessionid)
	if err != nil {
		return "", err
	}
	return session.auth, nil
}

// GetNick returns the nickname of |sessionid|, or the empty string if that
// session does not exist.
func (i *IRCServer) GetNick(sessionid types.RobustId) string {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	if s, ok := i.sessions[sessionid]; ok {
		return s.Nick
	}
	return ""
}

// GetStartId returns the first message id that could possibly be interesting
// for |sessionid| or the zero id.
func (i *IRCServer) GetStartId(sessionid types.RobustId) types.RobustId {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	if s, ok := i.sessions[sessionid]; ok {
		return s.startId
	}
	return types.RobustId{}
}

// ThrottleUntil returns the last activity of |sessionid| or the zero time.
func (i *IRCServer) ThrottleUntil(sessionid types.RobustId, cooloff time.Duration) time.Time {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	if s, ok := i.sessions[sessionid]; ok && !s.Server {
		// Reset throttlingExponent when the session was idle long enough.
		if time.Since(s.LastActivity) > cooloff {
			s.throttlingExponent = 0
		}
		delay := time.Duration(math.Pow(2, float64(s.throttlingExponent))) * time.Millisecond
		if delay < cooloff {
			s.throttlingExponent++
		}

		return s.LastActivity.Add(delay)
	}
	return time.Time{}
}

// LastPostMessage returns |sessionid|’s last processed client message id and
// the corresponding reply (for duplicate detection).
func (i *IRCServer) LastPostMessage(sessionid types.RobustId) (uint64, []byte) {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	if s, ok := i.sessions[sessionid]; ok {
		return s.lastClientMessageId, s.lastPostMessageReply
	}
	return 0, []byte{}
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
func (i *IRCServer) StillRelevant(ircmsg *irc.Message, prev, next logCursor) (bool, error) {
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

	return c.StillRelevant(ircmsg, prev, next)
}

// GetSessions returns a copy of sessions that can be used in the status
// handler (i.e. it goes stale, but doesn’t block other operations).
func (i *IRCServer) GetSessions() map[types.RobustId]Session {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
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

// Get wraps outputstream.Get to avoid making the outputstream instance
// public. All other methods should not be used, which is enforced this way.
func (i *IRCServer) Get(input types.RobustId) ([]*types.RobustMessage, bool) {
	return i.output.Get(input)
}

// Delete wraps outputstream.Delete to avoid making the outputstream instance
// public. All other methods should not be used, which is enforced this way.
func (i *IRCServer) Delete(input types.RobustId) {
	i.output.Delete(input)
}
