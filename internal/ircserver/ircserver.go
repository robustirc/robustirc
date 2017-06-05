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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/robustirc/internal/config"
	"github.com/robustirc/robustirc/internal/robust"
	"gopkg.in/sorcix/irc.v2"
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
	Id                robust.Id
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
	Created           int64

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
	sessions   map[robust.Id]*Session
	sessionsMu *sync.RWMutex

	// serverSessions is a slice that contains the IDs of all sessions that
	// represent server-to-server connections, so that they can efficiently be
	// added in e.g. interestJoin.
	serverSessions []uint64

	// nicks maps from nicknames in lower-case (e.g. NickToLower("sECuRE")) to
	// session pointers. Being able to quickly look up sessions based on their
	// nickname is handy to implement IRC commands efficiently.
	nicks map[lcNick]*Session

	// channels is a map containing the properties of every known channel (e.g.
	// topic or modes), keyed by the lower-case channel name (e.g.
	// ChanToLower(“#robustirc”)).
	channels map[lcChan]*channel

	svsholds map[lcNick]svshold

	// ServerPrefix is the prefix for output messages that come from the
	// server, as opposed to from a client.
	ServerPrefix *irc.Prefix

	// lastProcessed is the id of the last message fed into ProcessMessage.
	lastProcessed   robust.Id
	lastProcessedMu *sync.RWMutex

	// serverCreation is the time at which the IRCServer object was created.
	// Used for the RPL_CREATED message.
	ServerCreation time.Time

	// Config contains the network configuration.
	Config   config.Network
	ConfigMu *sync.RWMutex
}

// NewIRCServer returns a new IRC server.
func NewIRCServer(networkname string, serverCreation time.Time) *IRCServer {
	return &IRCServer{
		channels:        make(map[lcChan]*channel),
		svsholds:        make(map[lcNick]svshold),
		nicks:           make(map[lcNick]*Session),
		sessions:        make(map[robust.Id]*Session),
		sessionsMu:      &sync.RWMutex{},
		lastProcessedMu: &sync.RWMutex{},
		ServerPrefix:    &irc.Prefix{Name: networkname},
		ServerCreation:  serverCreation,
		Config:          config.DefaultConfig,
		ConfigMu:        &sync.RWMutex{},
	}
}

// UpdateLastMessage stores the clientmessageid of the last message in the
// corresponding session, so that duplicate messages are not persisted twice.
func (i *IRCServer) UpdateLastClientMessageID(msg *robust.Message) error {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	session, err := i.getSessionLocked(msg.Session)
	if err != nil {
		return err
	}
	session.LastActivity = msg.Timestamp()
	if !strings.HasPrefix(strings.ToLower(msg.Data), "ping") {
		session.LastNonPing = session.LastActivity
	}
	session.lastClientMessageId = msg.ClientMessageId
	return nil
}

// CreateSession creates a new session (equivalent to an IRC connection).
func (i *IRCServer) CreateSession(id robust.Id, auth string, timestamp time.Time) error {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()
	return i.createSessionLocked(id, auth, timestamp)
}

func (i *IRCServer) createSessionLocked(id robust.Id, auth string, timestamp time.Time) error {
	if got, limit := uint64(len(i.sessions)), i.SessionLimit(); got >= limit && limit > 0 {
		return ErrSessionLimitReached
	}
	i.sessions[id] = &Session{
		Id:           id,
		auth:         auth,
		Channels:     make(map[lcChan]bool),
		invitedTo:    make(map[lcChan]bool),
		Created:      timestamp.UnixNano(),
		LastActivity: timestamp,
		LastNonPing:  timestamp,
		svid:         "0",
	}
	return nil
}

// DeleteSession deletes the specified session. Called from the IRC server
// itself (when processing QUIT or KILL) or from the API (DELETE request coming
// from the bridge).
func (i *IRCServer) deleteSessionLocked(s *Session, msgid uint64) {
	for _, c := range i.channels {
		delete(c.nicks, NickToLower(s.Nick))

		i.maybeDeleteChannelLocked(c)
	}
	delete(i.nicks, NickToLower(s.Nick))
	// Instead of deleting the session here, we defer that to SendMessages, as
	// SendMessages calls the Interesting function of each reply (such as a
	// QUIT reply) and that function might still need access to the session to
	// determine where the reply should be sent to.
	s.deleted = true
}

// ExpireSessions returns DeleteSession robust.Messages for all sessions
// that are older than timeout. These messages are then applied to raft.
func (i *IRCServer) ExpireSessions() []*robust.Message {
	var deletes []*robust.Message

	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
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

		log.Printf("Expiring session %v (LastActivity: %v)", id, s.LastActivity)

		deletes = append(deletes, &robust.Message{
			Session: id,
			Type:    robust.DeleteSession,
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

func (i *IRCServer) maybeDeleteChannelLocked(c *channel) {
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
func (i *IRCServer) ProcessMessage(msg *robust.Message, ircmsg *irc.Message) *Replyctx {
	i.sessionsMu.Lock()
	defer i.sessionsMu.Unlock()

	// alias for convenience
	s := i.sessions[msg.Session]
	reply := &Replyctx{msgid: msg.Id.Id, session: s}

	if ircmsg == nil {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_UNKNOWNCOMMAND,
			Params:  []string{s.Nick, "Unknown command"},
		})
		return reply
	}

	command := strings.ToUpper(ircmsg.Command)

	messagesProcessed.WithLabelValues(command).Inc()

	if !s.loggedIn && !s.Server &&
		command != irc.NICK &&
		command != irc.USER &&
		command != irc.PASS &&
		command != irc.QUIT &&
		command != irc.SERVER {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTREGISTERED,
			Params:  []string{command, "You have not registered"},
		})
		if s.LastActivity.Sub(time.Unix(0, s.Created)) > 10*time.Minute {
			i.sendUser(s, reply, &irc.Message{
				Command: irc.ERROR,
				Params:  []string{"Closing Link: You have not registered within 10 minutes"},
			})
			i.deleteSessionLocked(s, reply.msgid)
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
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_UNKNOWNCOMMAND,
			Params:  []string{s.Nick, command, "Unknown command"},
		})
		return reply
	}

	if len(ircmsg.Params) < cmd.MinParams {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NEEDMOREPARAMS,
			Params:  []string{s.Nick, command, "Not enough parameters"},
		})
		return reply
	}

	cmd.Func(i, s, reply, ircmsg)
	return reply
}

func (i *IRCServer) SetLastProcessed(id robust.Id) {
	i.lastProcessedMu.Lock()
	defer i.lastProcessedMu.Unlock()
	i.lastProcessed = id
}

func (i *IRCServer) MaybeDeleteSession(session robust.Id) {
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
}

// GetSession returns a pointer to the session specified by 'id'.
//
// It returns ErrNoSuchSession when the session definitely does not exist
// (based on the timestamp), but ErrSessionNotYetSeen when the session might
// exist in the future, but was not yet seen on this IRC instance server
// (perhaps due to raft delay).
func (i *IRCServer) GetSession(id robust.Id) (*Session, error) {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	return i.getSessionLocked(id)
}

func (i *IRCServer) getSessionLocked(id robust.Id) (*Session, error) {
	s, ok := i.sessions[id]
	if ok {
		return s, nil
	}

	i.lastProcessedMu.RLock()
	defer i.lastProcessedMu.RUnlock()
	if i.lastProcessed.Id > id.Id {
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
func (i *IRCServer) GetAuth(sessionid robust.Id) (string, error) {
	session, err := i.GetSession(sessionid)
	if err != nil {
		return "", err
	}
	return session.auth, nil
}

// GetNick returns the nickname of |sessionid|, or the empty string if that
// session does not exist.
func (i *IRCServer) GetNick(sessionid robust.Id) string {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	if s, ok := i.sessions[sessionid]; ok {
		return s.Nick
	}
	return ""
}

// ThrottleUntil returns the last activity of |sessionid| or the zero time.
func (i *IRCServer) ThrottleUntil(sessionid robust.Id) time.Time {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
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
func (i *IRCServer) LastPostMessage(sessionid robust.Id) uint64 {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	if s, ok := i.sessions[sessionid]; ok {
		return s.lastClientMessageId
	}
	return 0
}

// GetSessions returns a copy of sessions that can be used in the status
// handler (i.e. it goes stale, but doesn’t block other operations).
func (i *IRCServer) GetSessions() map[robust.Id]Session {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	result := make(map[robust.Id]Session, len(i.sessions))
	for id, session := range i.sessions {
		result[id] = *session
	}
	return result
}

// NumSessions returns the current number of sessions.
func (i *IRCServer) NumSessions() int {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	return len(i.sessions)
}

// NumSessions returns the current number of channels.
func (i *IRCServer) NumChannels() int {
	// TODO: replace this with a more appropriate lock
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()

	return len(i.channels)
}

// Replyctx is a reply context, i.e. information necessary when replying to an
// IRC message. A reply context object will be passed to all cmd* functions and
// the send* functions use it to keep track of the replyid for example.
type Replyctx struct {
	msgid    uint64
	replyid  uint64
	session  *Session
	Messages []*robust.Message

	// lastmsg tracks the last sent message, so that send() can return the same
	// message multiple times when being called in a continuation.
	lastmsg *irc.Message
}

// send converts |msg| into a robust.Message and appends it to |reply|.
func (i *IRCServer) send(reply *Replyctx, msg *irc.Message) *robust.Message {
	if reply.lastmsg == msg {
		return reply.Messages[len(reply.Messages)-1]
	}

	reply.replyid++

	robustmsg := &robust.Message{
		// The IDs must be the same across servers.
		Id: robust.Id{
			Id:    reply.msgid,
			Reply: reply.replyid,
		},
		Data:           string(msg.Bytes()),
		InterestingFor: make(map[uint64]bool),
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

func (i *IRCServer) SessionLimit() uint64 {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.MaxSessions
}

func (i *IRCServer) ChannelLimit() uint64 {
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	return i.Config.MaxChannels
}
