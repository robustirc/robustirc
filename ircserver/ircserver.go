// Package ircserver implements an IRC server which strictly adheres to a
// processing model where output is only ever generated in response to input,
// and only depends on state that is local to the IRC server.
//
// That means the output of two IRC server instances will be byte-for-byte
// identical if you feed them exactly the same input (in the same order), which
// is what RobustIRC does when recovering from a raft snapshot.
package ircserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"

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

	// ErrSessionNotYetSeen is returned when the session was not (yet?) seen on
	// this follower. We cannot say with confidence that it does not exist.
	ErrSessionNotYetSeen = errors.New("Session not yet seen")

	// ErrNoSuchSession is returned when the session definitely does not exist.
	ErrNoSuchSession = errors.New("No such session")

	// ErrSessionLimitReached is returned when the number of sessions exceeds the configured limit.
	ErrSessionLimitReached = errors.New("MaxSessions limit reached")

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

	captchasVerified = prometheus.NewCounter(
		prometheus.CounterOpts{
			Subsystem: "captcha",
			Name:      "captchas_verified",
			Help:      "Number of CAPTCHAs successfully verified",
		},
	)

	captchasFailed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Subsystem: "captcha",
			Name:      "captchas_failed",
			Help:      "Number of non-empty CAPTCHAs which failed verification",
		},
	)
)

func init() {
	prometheus.MustRegister(messagesProcessed)
	prometheus.MustRegister(captchasVerified)
	prometheus.MustRegister(captchasFailed)
}

// lcChan is a lower-case channel name, e.g. “#chaos-hd”, even when the user
// sent “JOIN #Chaos-HD”. It is used to enforce using ChanToLower() on keys of
// various maps.
type lcChan string

// lcNick is a lower-case nickname, e.g. “secure”, even when the user sent
// “NICK sECuRE”. It is used to enforce using NickToLower() on keys of various
// maps.
type lcNick string

type Session struct {
	Id                types.RobustId
	auth              string
	loggedIn          bool
	Nick              string
	Username          string
	Realname          string
	Channels          map[lcChan]bool
	LastActivity      time.Time
	LastNonPing       time.Time
	LastSolvedCaptcha time.Time
	Operator          bool
	AwayMsg           string

	// throttlingExponent starts at 0 and is increased on every
	// subsequent message until 2^throttlingExponent ≥
	// ircServer.Config.PostMessageCooloff.  It will be reset once the
	// user does not send messages for
	// ircServer.Config.PostMessageCooloff.
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
	lastClientMessageId uint64

	ircPrefix irc.Prefix
	// deleted gets set by DeleteSession and used by SendMessages. Refer to the
	// DeleteSession comment.
	deleted bool
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

// svshold stores nickname reservations set by services, e.g. for reserving the
// nickname a while after a person fails to identify.
type svshold struct {
	added    time.Time
	duration time.Duration
	reason   string
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

	svsholds map[lcNick]svshold

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

	// Config contains the network configuration.
	Config   config.Network
	ConfigMu *sync.RWMutex
}

// NewIRCServer returns a new IRC server.
func NewIRCServer(raftdir, networkname string, serverCreation time.Time) *IRCServer {
	os, err := outputstream.NewOutputStream(raftdir)
	if err != nil {
		log.Panicf("Could not create new outputstream: %v\n", err)
	}
	return &IRCServer{
		channels:        make(map[lcChan]*channel),
		svsholds:        make(map[lcNick]svshold),
		nicks:           make(map[lcNick]*Session),
		sessions:        make(map[types.RobustId]*Session),
		sessionsMu:      &sync.RWMutex{},
		lastProcessedMu: &sync.RWMutex{},
		output:          os,
		ServerPrefix:    &irc.Prefix{Name: networkname},
		ServerCreation:  serverCreation,
		Config:          config.DefaultConfig,
		ConfigMu:        &sync.RWMutex{},
	}
}

// Close closes all resources this IRCServer holds (e.g. the
// outputstream). After calling Close(), you must not use this
// IRCServer object anymore.
func (i *IRCServer) Close() error {
	return i.output.Close()
}

// NewRobustMessageId returns an id that is guaranteed to be higher
// than the last processed message (it panics otherwise) so that
// timestamp drift is loudly complained about instead of silently
// accepted to the point where it breaks the network’s regular
// operation.
//
// NewRobustMessageId should only be called while node.State() ==
// raft.Leader, otherwise it might panic due to time drift in the
// network, leading to the node exiting (and being restarted).
func (i *IRCServer) NewRobustMessageId() types.RobustId {
	unixnano := time.Now().UnixNano()
	i.lastProcessedMu.RLock()
	defer i.lastProcessedMu.RUnlock()
	if unixnano < i.lastProcessed.Id {
		panic(fmt.Sprintf("Assumption violated: current time %d is older than the timestamp of the last processed message (%d)", unixnano, i.lastProcessed.Id))
	}
	return types.RobustId{Id: unixnano}
}

// UpdateLastMessage stores the clientmessageid of the last message in the
// corresponding session, so that duplicate messages are not persisted twice.
func (i *IRCServer) UpdateLastClientMessageID(msg *types.RobustMessage) error {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	session, err := i.getSessionLocked(msg.Session)
	if err != nil {
		return err
	}
	session.LastActivity = time.Unix(0, msg.Id.Id)
	if !strings.HasPrefix(strings.ToLower(msg.Data), "ping") {
		session.LastNonPing = session.LastActivity
	}
	session.lastClientMessageId = msg.ClientMessageId
	return nil
}

// CreateSession creates a new session (equivalent to an IRC connection).
func (i *IRCServer) CreateSession(id types.RobustId, auth string) error {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	if uint64(len(i.sessions)) >= i.Config.MaxSessions && i.Config.MaxSessions > 0 {
		return ErrSessionLimitReached
	}
	i.sessions[id] = &Session{
		Id:           id,
		auth:         auth,
		startId:      i.output.LastSeen(),
		Channels:     make(map[lcChan]bool),
		invitedTo:    make(map[lcChan]bool),
		LastActivity: time.Unix(0, id.Id),
		LastNonPing:  time.Unix(0, id.Id),
		svid:         "0",
	}
	return nil
}

// DeleteSession deletes the specified session. Called from the IRC server
// itself (when processing QUIT or KILL) or from the API (DELETE request coming
// from the bridge).
func (i *IRCServer) DeleteSession(s *Session, msgid int64) {
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
func (i *IRCServer) ExpireSessions() []*types.RobustMessage {
	var deletes []*types.RobustMessage

	timeout := time.Duration(i.Config.SessionExpiration)

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

		deletes = append(deletes, &types.RobustMessage{
			Session: id,
			Type:    types.RobustDeleteSession,
			Data:    fmt.Sprintf("Ping timeout (%v)", timeout),
		})
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
			!strings.HasPrefix(part, "captcha=") &&
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
func (i *IRCServer) ProcessMessage(id types.RobustId, session types.RobustId, message *irc.Message) *Replyctx {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()

	// alias for convenience
	s := i.sessions[session]
	reply := &Replyctx{msgid: id.Id, session: s}

	if message == nil {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_UNKNOWNCOMMAND,
			Params:   []string{s.Nick},
			Trailing: "Unknown command",
		})
		return reply
	}

	command := strings.ToUpper(message.Command)

	messagesProcessed.WithLabelValues(command).Inc()

	if !s.loggedIn && !s.Server &&
		command != irc.NICK &&
		command != irc.USER &&
		command != irc.PASS &&
		command != irc.QUIT &&
		command != irc.SERVER {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTREGISTERED,
			Params:   []string{command},
			Trailing: "You have not registered",
		})
		if s.LastActivity.Sub(time.Unix(0, s.Id.Id)) > 10*time.Minute {
			i.sendUser(s, reply, &irc.Message{
				Command:  irc.ERROR,
				Trailing: "Closing Link: You have not registered within 10 minutes",
			})
			i.DeleteSession(s, reply.msgid)
		}
		return reply
	}

	var serverPrefix string
	if s.Server {
		serverPrefix = "server_"
	}
	cmd, ok := Commands[serverPrefix+command]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_UNKNOWNCOMMAND,
			Params:   []string{s.Nick, command},
			Trailing: "Unknown command",
		})
		return reply
	}

	if len(message.Params) < cmd.MinParams {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, command},
			Trailing: "Not enough parameters",
		})
		return reply
	}

	cmd.Func(i, s, reply, message)
	return reply
}

func (i *IRCServer) setLastProcessed(id types.RobustId) {
	i.lastProcessedMu.Lock()
	defer i.lastProcessedMu.Unlock()
	i.lastProcessed = id
}

// SendMessages appends the specified batch of messages to the output, marking
// them as a response to the incoming message with id 'id' and associating them
// with session 'session'. IRC clients will eventually receive these messages
// by calling GetNext.
func (i *IRCServer) SendMessages(reply *Replyctx, session types.RobustId, id int64) {
	i.setLastProcessed(types.RobustId{Id: id})

	defer func() {
		i.sessionsMu.Lock()
		defer i.sessionsMu.Unlock()
		if s, ok := i.sessions[session]; ok {
			// services can delete both, individual service sessions (e.g. a
			// BotServ-created bot) and users (e.g. with NickServ’s RECOVER
			// command), so just check all sessions.
			if s.Server || s.Operator {
				for id, session := range i.sessions {
					if session.deleted {
						delete(i.sessions, id)
					}
				}
			}
			if s.deleted {
				delete(i.sessions, session)
			}
		}
	}()

	if len(reply.Messages) == 0 {
		return
	}

	converted := make([]outputstream.Message, 0, len(reply.Messages))
	for _, msg := range reply.Messages {
		converted = append(converted, outputstream.Message{
			Id:             msg.Id,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		})
	}
	if err := i.output.Add(converted); err != nil {
		log.Panicf("Could not add messages to outputstream: %v\n", err)
	}
}

// GetSession returns a pointer to the session specified by 'id'.
//
// It returns ErrNoSuchSession when the session definitely does not exist
// (based on the timestamp), but ErrSessionNotYetSeen when the session might
// exist in the future, but was not yet seen on this IRC instance server
// (perhaps due to raft delay).
func (i *IRCServer) GetSession(id types.RobustId) (*Session, error) {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	return i.getSessionLocked(id)
}

func (i *IRCServer) getSessionLocked(id types.RobustId) (*Session, error) {
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
func (i *IRCServer) ThrottleUntil(sessionid types.RobustId) time.Time {
	cooloff := time.Duration(i.Config.PostMessageCooloff)
	if cooloff == 0 {
		return time.Time{}
	}
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
func (i *IRCServer) LastPostMessage(sessionid types.RobustId) uint64 {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	if s, ok := i.sessions[sessionid]; ok {
		return s.lastClientMessageId
	}
	return 0
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

func (i *IRCServer) outputToRobustMessages(msgs []outputstream.Message) []*types.RobustMessage {
	result := make([]*types.RobustMessage, 0, len(msgs))
	for _, msg := range msgs {
		result = append(result, &types.RobustMessage{
			Id:             msg.Id,
			Type:           types.RobustIRCToClient,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		})
	}
	return result
}

// GetNext wraps outputstream.GetNext and converts the messages into
// RobustMessages.
func (i *IRCServer) GetNext(ctx context.Context, lastseen types.RobustId) []*types.RobustMessage {
	return i.outputToRobustMessages(i.output.GetNext(ctx, lastseen))
}

// Get wraps outputstream.GetNext and converts the messages into
// RobustMessages.
func (i *IRCServer) Get(input types.RobustId) ([]*types.RobustMessage, bool) {
	msgs, ok := i.output.Get(input)
	return i.outputToRobustMessages(msgs), ok
}

// Delete wraps outputstream.Delete to avoid making the outputstream instance
// public. Some methods of outputstream must not be used, which is enforced
// this way.
func (i *IRCServer) Delete(input types.RobustId) error {
	return i.output.Delete(input)
}

// InterruptGetNext wraps outputstream.InterruptGetNext to avoid making the
// outputstream instance public. Some methods of outputstream must not be used,
// which is enforced this way.
func (i *IRCServer) InterruptGetNext() {
	i.output.InterruptGetNext()
}

// Replyctx is a reply context, i.e. information necessary when replying to an
// IRC message. A reply context object will be passed to all cmd* functions and
// the send* functions use it to keep track of the replyid for example.
type Replyctx struct {
	msgid    int64
	replyid  int64
	session  *Session
	Messages []*types.RobustMessage

	// lastmsg tracks the last sent message, so that send() can return the same
	// message multiple times when being called in a continuation.
	lastmsg *irc.Message
}

// send converts |msg| into a RobustMessage and appends it to |reply|.
func (i *IRCServer) send(reply *Replyctx, msg *irc.Message) *types.RobustMessage {
	if reply.lastmsg == msg {
		return reply.Messages[len(reply.Messages)-1]
	}

	reply.replyid++

	robustmsg := &types.RobustMessage{
		// The IDs must be the same across servers.
		Id: types.RobustId{
			Id:    reply.msgid,
			Reply: reply.replyid,
		},
		Data:           string(msg.Bytes()),
		InterestingFor: make(map[int64]bool),
	}

	reply.Messages = append(reply.Messages, robustmsg)
	reply.lastmsg = msg

	return robustmsg
}

// sendUser sends |msg| to |user|.
func (i *IRCServer) sendUser(user *Session, reply *Replyctx, msg *irc.Message) *irc.Message {
	robustmsg := i.send(reply, msg)
	robustmsg.InterestingFor[user.Id.Id] = true
	return msg
}

// sendCommonChannels sends |msg| to all users which are in one of the channels
// on which |user| is in.
func (i *IRCServer) sendCommonChannels(user *Session, reply *Replyctx, msg *irc.Message) *irc.Message {
	robustmsg := i.send(reply, msg)
	for channelname := range user.Channels {
		c, ok := i.channels[channelname]
		if !ok {
			continue
		}
		for nick := range c.nicks {
			robustmsg.InterestingFor[i.nicks[nick].Id.Id] = true
		}
	}
	return msg
}

// sendChannel sends |msg| to all users who are in |c|.
func (i *IRCServer) sendChannel(c *channel, reply *Replyctx, msg *irc.Message) *irc.Message {
	robustmsg := i.send(reply, msg)
	for nick := range c.nicks {
		robustmsg.InterestingFor[i.nicks[nick].Id.Id] = true
	}
	return msg
}

// sendChannelButOne sends |msg| to all users who are in |c|, except for |user|
func (i *IRCServer) sendChannelButOne(c *channel, user *Session, reply *Replyctx, msg *irc.Message) *irc.Message {
	robustmsg := i.send(reply, msg)
	for nick := range c.nicks {
		session := i.nicks[nick]
		if session == user {
			continue
		}
		robustmsg.InterestingFor[session.Id.Id] = true
	}
	return msg
}

// sendServices sends |msg| to the IRC services.
func (i *IRCServer) sendServices(reply *Replyctx, msg *irc.Message) *irc.Message {
	robustmsg := i.send(reply, msg)
	for _, serverid := range i.serverSessions {
		robustmsg.InterestingFor[serverid] = true
	}
	return msg
}

func (i *IRCServer) TrustedBridge(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.TrustedBridges[authHeader]
}

func (i *IRCServer) captchaConfigured() bool {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.CaptchaURL != "" && i.Config.CaptchaHMACSecret != nil
}

func (i *IRCServer) captchaRequiredForLogin() bool {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.CaptchaRequiredForLogin
}

func (i *IRCServer) generateCaptchaURL(s *Session, purpose string) string {
	challenge := []byte(s.auth[:8])

	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()

	mac := hmac.New(sha256.New, i.Config.CaptchaHMACSecret)
	mac.Write([]byte(purpose))
	mac.Write(challenge)
	parts := strings.Join([]string{
		base64.StdEncoding.EncodeToString([]byte(purpose)),
		base64.StdEncoding.EncodeToString(challenge),
		base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}, ".")

	u, _ := url.Parse(i.Config.CaptchaURL)
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = parts
	return u.String()
}

func (i *IRCServer) verifyCaptcha(s *Session, captcha string) error {
	// Don’t bother users with captchas for one minute after solving one.
	if s.LastActivity.Sub(s.LastSolvedCaptcha) < 1*time.Minute {
		return nil
	}

	if captcha == "" {
		return errors.New("no captcha specified")
	}

	if err := i.verifyCaptchaNonEmpty(s, captcha); err != nil {
		captchasFailed.Inc()
		return err
	}

	captchasVerified.Inc()
	s.LastSolvedCaptcha = s.LastActivity
	return nil
}

func (i *IRCServer) verifyCaptchaNonEmpty(s *Session, captcha string) error {
	parts := strings.Split(captcha, ".")
	if got, want := len(parts), 3; got != want {
		return fmt.Errorf("Unexpected number of challenge parts: got %d, want %d", got, want)
	}

	decoded := make([][]byte, 3)
	var err error
	for idx, part := range parts {
		decoded[idx], err = base64.StdEncoding.DecodeString(part)
		if err != nil {
			return err
		}
	}

	purpose := decoded[0]
	challenge := decoded[1]
	vmac := decoded[2]

	if !strings.HasPrefix(string(purpose), "okay:") {
		return errors.New("challenge purpose does not start with okay: (replay?)")
	}

	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()

	mac := hmac.New(sha256.New, i.Config.CaptchaHMACSecret)
	mac.Write(purpose)
	mac.Write(challenge)
	if !hmac.Equal(vmac, mac.Sum(nil)) {
		return errors.New("captcha could not be verified")
	}

	// purpose is okay:<command>:<lastactivity>:<argument>
	// e.g. okay:join:123:#robustirc
	// or okay:login:123:
	purposeparts := strings.Split(string(purpose), ":")
	if got, want := len(purposeparts), 4; got != want {
		return fmt.Errorf("Unexpected number of purpose parts: got %d, want %d", got, want)
	}

	lastActivity, err := strconv.ParseInt(purposeparts[2], 10, 64)
	if err != nil {
		return err
	}

	if s.LastActivity.Sub(time.Unix(0, lastActivity)) > 5*time.Minute {
		return errors.New("challenge too old")
	}

	return nil
}

func (i *IRCServer) channelLimit() uint64 {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.MaxChannels
}
