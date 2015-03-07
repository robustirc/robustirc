package ircserver

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
)

var (
	commands = make(map[string]*ircCommand)
)

type ircCommand struct {
	Func func(*IRCServer, *Session, *irc.Message) []*irc.Message

	// Interesting returns a map that determines to which session a message
	// should be sent.
	Interesting func(*IRCServer, types.RobustId, *irc.Message) map[int64]bool

	// StillRelevant is used during compaction. If it returns true, the message
	// is kept, otherwise it will be deleted.
	StillRelevant func(*irc.Message, logCursor, logCursor) (bool, error)

	// MinParams ensures that enough parameters were specified.
	// irc.ERR_NEEDMOREPARAMS is returned in case less than MinParams
	// parameters were found, otherwise, Func is called.
	MinParams int
}

func init() {
	// Keep this list ordered the same way the functions below are ordered.
	commands["PING"] = &ircCommand{
		Func:          (*IRCServer).cmdPing,
		StillRelevant: neverRelevant,
	}
	commands["NICK"] = &ircCommand{
		Func:          (*IRCServer).cmdNick,
		Interesting:   (*IRCServer).interestNick,
		StillRelevant: relevantNick,
	}
	commands["USER"] = &ircCommand{
		Func:          (*IRCServer).cmdUser,
		MinParams:     3,
		StillRelevant: relevantUser,
	}
	commands["JOIN"] = &ircCommand{
		Func:          (*IRCServer).cmdJoin,
		MinParams:     1,
		Interesting:   (*IRCServer).interestJoin,
		StillRelevant: relevantJoin,
	}
	commands["PART"] = &ircCommand{
		Func:          (*IRCServer).cmdPart,
		MinParams:     1,
		Interesting:   (*IRCServer).interestPart,
		StillRelevant: relevantPart,
	}
	commands["KICK"] = &ircCommand{
		Func:        (*IRCServer).cmdKick,
		MinParams:   2,
		Interesting: (*IRCServer).interestKick,
	}
	commands["QUIT"] = &ircCommand{
		Func:        (*IRCServer).cmdQuit,
		Interesting: (*IRCServer).interestQuit,
		// TODO: the bridge always sends DestroySession, but third-party clients may not. so, better keep QUITs?
		StillRelevant: neverRelevant,
	}
	commands["PRIVMSG"] = &ircCommand{
		Func:          (*IRCServer).cmdPrivmsg,
		Interesting:   (*IRCServer).interestPrivmsg,
		StillRelevant: neverRelevant,
	}
	commands["NOTICE"] = &ircCommand{
		Func:          (*IRCServer).cmdPrivmsg,
		Interesting:   (*IRCServer).interestPrivmsg,
		StillRelevant: neverRelevant,
	}
	commands["MODE"] = &ircCommand{
		Func:        (*IRCServer).cmdMode,
		MinParams:   1,
		Interesting: (*IRCServer).interestMode,
	}
	commands["WHO"] = &ircCommand{
		Func:          (*IRCServer).cmdWho,
		StillRelevant: neverRelevant,
	}
	commands["OPER"] = &ircCommand{Func: (*IRCServer).cmdOper, MinParams: 2}
	commands["KILL"] = &ircCommand{Func: (*IRCServer).cmdKill, MinParams: 1}
	commands["AWAY"] = &ircCommand{Func: (*IRCServer).cmdAway}
	commands["TOPIC"] = &ircCommand{
		Func:          (*IRCServer).cmdTopic,
		MinParams:     1,
		Interesting:   (*IRCServer).interestTopic,
		StillRelevant: relevantTopic,
	}
	commands["MOTD"] = &ircCommand{
		Func:          (*IRCServer).cmdMotd,
		StillRelevant: neverRelevant,
	}
	commands["WHOIS"] = &ircCommand{
		Func:          (*IRCServer).cmdWhois,
		MinParams:     1,
		StillRelevant: neverRelevant,
	}
	commands["LIST"] = &ircCommand{
		Func:          (*IRCServer).cmdList,
		StillRelevant: neverRelevant,
	}
	commands["INVITE"] = &ircCommand{
		Func:        (*IRCServer).cmdInvite,
		Interesting: (*IRCServer).interestInvite,
		MinParams:   2,
	}
	commands["USERHOST"] = &ircCommand{
		Func:          (*IRCServer).cmdUserhost,
		StillRelevant: neverRelevant,
		MinParams:     1,
	}

	if os.Getenv("ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND") == "1" {
		commands["PANIC"] = &ircCommand{
			Func: func(i *IRCServer, s *Session, msg *irc.Message) []*irc.Message {
				panic("PANIC called")
			},
		}
	}
	commands["PASS"] = &ircCommand{Func: (*IRCServer).cmdPass}
}

func neverRelevant(m *irc.Message, prev, next logCursor) (bool, error) {
	return false, nil
}

func multipleChannels(targets string) map[lcChan]bool {
	channelnames := strings.Split(targets, ",")
	lcnames := make(map[lcChan]bool, len(channelnames))
	for _, name := range channelnames {
		lcnames[ChanToLower(name)] = true
	}
	return lcnames
}

func anyChanInChannels(needles, haystack map[lcChan]bool) bool {
	for needle, _ := range needles {
		if haystack[needle] {
			return true
		}
	}
	return false
}

func (i *IRCServer) cmdPing(s *Session, msg *irc.Message) []*irc.Message {
	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOORIGIN,
			Params:   []string{s.Nick},
			Trailing: "No origin specified",
		}}
	}
	return []*irc.Message{&irc.Message{
		Command: irc.PONG,
		Params:  []string{msg.Params[0]},
	}}
}

// login is called by either cmdNick or cmdUser, depending on which message the
// client sends last.
func (i *IRCServer) login(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message

	// TODO(secure): send 002, 003, 004, 251, 252, 254, 255, 265, 266
	replies = append(replies, &irc.Message{
		Command:  irc.RPL_WELCOME,
		Params:   []string{s.Nick},
		Trailing: "Welcome to RobustIRC!",
	})

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_YOURHOST,
		Params:   []string{s.Nick},
		Trailing: "Your host is " + i.ServerPrefix.Name,
	})

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_CREATED,
		Params:   []string{s.Nick},
		Trailing: "This server was created " + i.ServerCreation.UTC().String(),
	})

	replies = append(replies, &irc.Message{
		Command: irc.RPL_MYINFO,
		Params:  []string{s.Nick},
		// TODO(secure): actually support these modes.
		Trailing: i.ServerPrefix.Name + " v1 i nst",
	})

	// send ISUPPORT as per http://www.irc.org/tech_docs/draft-brocklesby-irc-isupport-03.txt
	replies = append(replies, &irc.Message{
		Command: "005",
		Params: []string{
			"CHANTYPES=#",
			"CHANNELLEN=" + maxChannelLen,
			"NICKLEN=" + maxNickLen,
			"MODES=1",
			"PREFIX=(o)@",
		},
		Trailing: "are supported by this server",
	})

	replies = append(replies, &irc.Message{
		Prefix:  &irc.Prefix{},
		Command: irc.NICK,
		Params: []string{
			s.Nick,
			"1", // hopcount (ignored by anope)
			"1", // timestamp
			s.Username,
			s.ircPrefix.Host,
			i.ServerPrefix.Name,
			s.svid,
			"+",
		},
		Trailing: s.Realname,
	})

	replies = append(replies, i.cmdMotd(s, msg)...)

	return replies
}

func relevantNick(msg *irc.Message, prev, next logCursor) (bool, error) {
	if len(msg.Params) < 1 {
		return false, nil
	}

	for {
		nmsg, err := next()
		if err != nil {
			if err == CursorEOF {
				break
			}
			return true, err
		}
		// Found a USER message. This NICK command is thus the first one and must not be compacted.
		if nmsg.Command == irc.USER {
			return true, nil
		}
		// TOPIC relies on the NICK.
		if nmsg.Command == irc.TOPIC {
			return true, nil
		}
		// There is a newer NICK command, so discard this one.
		if nmsg.Command == irc.NICK {
			return false, nil
		}
	}

	return true, nil
}

func (i *IRCServer) interestNick(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	// everyone who is in any of the channels the nick is in
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	// Send NICK message in server-to-server format only to servers.
	if len(msg.Params) > 1 {
		return result
	}

	s := i.sessions[sessionid]

	result[i.nicks[NickToLower(msg.Trailing)].Id.Id] = true

	for channelname, _ := range s.Channels {
		channel := i.channels[channelname]
		for nick, _ := range channel.nicks {
			result[i.nicks[nick].Id.Id] = true
		}
	}

	return result
}

func (i *IRCServer) cmdNick(s *Session, msg *irc.Message) []*irc.Message {
	oldPrefix := s.ircPrefix

	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NONICKNAMEGIVEN,
			Trailing: "No nickname given",
		}}
	}

	if !IsValidNickname(msg.Params[0]) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_ERRONEUSNICKNAME,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "Erroneous nickname",
		}}
	}

	if _, ok := i.nicks[NickToLower(msg.Params[0])]; ok || IsServicesNickname(msg.Params[0]) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NICKNAMEINUSE,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "Nickname is already in use",
		}}
	}
	loggedIn := s.loggedIn()
	oldNick := NickToLower(s.Nick)
	s.Nick = msg.Params[0]
	i.nicks[NickToLower(s.Nick)] = s
	if oldNick != "" {
		delete(i.nicks, oldNick)
		for _, c := range i.channels {
			// Check ok to ensure we never assign the default value (<nil>).
			if modes, ok := c.nicks[oldNick]; ok {
				c.nicks[NickToLower(s.Nick)] = modes
			}
			delete(c.nicks, oldNick)
		}
	}
	s.updateIrcPrefix()
	if oldNick != "" {
		return []*irc.Message{&irc.Message{
			Prefix:   &oldPrefix,
			Command:  irc.NICK,
			Trailing: msg.Params[0],
		}}
	}

	if !loggedIn && s.loggedIn() {
		return i.login(s, msg)
	} else {
		return []*irc.Message{}
	}
}

func relevantUser(msg *irc.Message, prev, next logCursor) (bool, error) {
	if len(msg.Params) < 1 {
		return false, nil
	}

	for {
		pmsg, err := prev()
		if err != nil {
			if err == CursorEOF {
				break
			}
			return true, err
		}
		// There already was a USER message, so discard this one.
		if pmsg.Command == irc.USER {
			return false, nil
		}
	}

	return true, nil
}

func (i *IRCServer) cmdUser(s *Session, msg *irc.Message) []*irc.Message {
	loggedIn := s.loggedIn()
	// We keep the username (so that bans are more effective) and realname
	// (some people actually set it and look at it).
	s.Username = msg.Params[0]
	s.Realname = msg.Trailing
	s.updateIrcPrefix()
	if !loggedIn && s.loggedIn() {
		return i.login(s, msg)
	}
	return []*irc.Message{}
}

func (i *IRCServer) interestJoin(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	// everyone who currently is in the channel
	result := make(map[int64]bool)

	// If this is a server-to-server prefix, don’t send the message to clients.
	if msg.Prefix.User == "" {
		return result
	}

	channel := i.channels[ChanToLower(msg.Trailing)]
	for nick, _ := range channel.nicks {
		result[i.nicks[nick].Id.Id] = true
	}

	return result
}

func relevantJoin(msg *irc.Message, prev, next logCursor) (bool, error) {
	if len(msg.Params) < 1 {
		return false, nil
	}

	lcnames := multipleChannels(msg.Params[0])
	for {
		nmsg, err := next()
		if err != nil {
			if err == CursorEOF {
				break
			}
			return true, err
		}
		if nmsg.Command == irc.TOPIC && lcnames[ChanToLower(nmsg.Params[0])] {
			return true, nil
		}
		if nmsg.Command == irc.PART {
			for channelname, _ := range multipleChannels(nmsg.Params[0]) {
				delete(lcnames, channelname)
			}
			if len(lcnames) == 0 {
				return false, nil
			}
		}
	}

	return true, nil
}

func (i *IRCServer) cmdJoin(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		if !IsValidChannel(channelname) {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "No such channel",
			})
			continue
		}
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			i.channels[ChanToLower(channelname)] = c
		} else if c.modes['i'] && !s.invitedTo[ChanToLower(channelname)] {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_INVITEONLYCHAN,
				Params:   []string{s.Nick, c.name},
				Trailing: "Cannot join channel (+i)",
			})
			continue
		}
		if _, ok := c.nicks[NickToLower(s.Nick)]; ok {
			continue
		}
		c.nicks[NickToLower(s.Nick)] = &[maxChanMemberStatus]bool{}
		// If the channel did not exist before, the first joining user becomes a
		// channel operator.
		if !ok {
			c.nicks[NickToLower(s.Nick)][chanop] = true
		}
		s.Channels[ChanToLower(channelname)] = true

		nicks := make([]string, 0, len(c.nicks))
		for nick, perms := range c.nicks {
			var prefix string
			if perms[chanop] {
				prefix = prefix + string('@')
			}
			nicks = append(nicks, prefix+i.nicks[nick].Nick)
		}

		sort.Strings(nicks)

		replies = append(replies, &irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  irc.JOIN,
			Trailing: channelname,
		})
		var prefix string
		if c.nicks[NickToLower(s.Nick)][chanop] {
			prefix = prefix + string('@')
		}
		replies = append(replies, &irc.Message{
			Command:  "SJOIN",
			Params:   []string{"1", channelname},
			Trailing: prefix + s.Nick,
		})
		// Integrate the topic response by simulating a TOPIC command.
		replies = append(replies, i.cmdTopic(s, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})...)
		// TODO(secure): why the = param?
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_NAMREPLY,
			Params:   []string{s.Nick, "=", channelname},
			Trailing: strings.Join(nicks, " "),
		})
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_ENDOFNAMES,
			Params:   []string{s.Nick, channelname},
			Trailing: "End of /NAMES list.",
		})
	}

	return replies
}

func (i *IRCServer) interestKick(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	channel, ok := i.channels[ChanToLower(msg.Params[0])]
	if ok {
		for nick, _ := range channel.nicks {
			result[i.nicks[nick].Id.Id] = true
		}
	}

	// Do send KICK messages to the kicked user (who, by now, is not in the
	// channel anymore).
	result[i.nicks[NickToLower(msg.Params[1])].Id.Id] = true
	return result
}

func (i *IRCServer) cmdKick(s *Session, msg *irc.Message) []*irc.Message {
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "No such nick/channel",
		}}
	}

	perms, ok := c.nicks[NickToLower(s.Nick)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "You're not on that channel",
		}}
	}

	if !perms[chanop] {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, channelname},
			Trailing: "You're not channel operator",
		}}
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_USERNOTINCHANNEL,
			Params:   []string{s.Nick, msg.Params[1], channelname},
			Trailing: "They aren't on that channel",
		}}
	}

	// Must exist since c.nicks contains the nick.
	session, _ := i.nicks[NickToLower(msg.Params[1])]

	// TODO(secure): reduce code duplication with cmdPart()
	delete(c.nicks, NickToLower(msg.Params[1]))
	i.maybeDeleteChannel(c)
	delete(session.Channels, ChanToLower(channelname))
	return []*irc.Message{&irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.KICK,
		Params:   []string{msg.Params[0], msg.Params[1]},
		Trailing: msg.Trailing,
	}}
}

func (i *IRCServer) interestPart(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	channel, ok := i.channels[ChanToLower(msg.Params[0])]
	if ok {
		for nick, _ := range channel.nicks {
			result[i.nicks[nick].Id.Id] = true
		}
	}

	// Do send PART messages back to the sender (who, by now, is not in the
	// channel anymore).
	result[sessionid.Id] = true
	return result
}

func relevantPart(msg *irc.Message, prev, next logCursor) (bool, error) {
	if len(msg.Params) < 1 {
		return false, nil
	}

	lcnames := multipleChannels(msg.Params[0])
	for {
		pmsg, err := prev()
		if err != nil {
			if err == CursorEOF {
				break
			}
			return true, err
		}
		if pmsg.Command == irc.JOIN && anyChanInChannels(lcnames, multipleChannels(pmsg.Params[0])) {
			return true, nil
		}
	}

	return false, nil
}

func (i *IRCServer) cmdPart(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "No such channel",
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "You're not on that channel",
			})
			continue
		}

		delete(c.nicks, NickToLower(s.Nick))
		i.maybeDeleteChannel(c)
		delete(s.Channels, ChanToLower(channelname))
		replies = append(replies, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.PART,
			Params:  []string{channelname},
		})
	}

	return replies
}

func (i *IRCServer) interestQuit(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	// everyone who is in any of the channels the nick is in
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	s := i.sessions[sessionid]

	for channelname, _ := range s.Channels {
		channel, ok := i.channels[channelname]
		// If the channel does not exist anymore, it was deleted with this QUIT
		// message, so there cannot be anyone else in there to receive our
		// message.
		if !ok {
			continue
		}
		for nick, _ := range channel.nicks {
			result[i.nicks[nick].Id.Id] = true
		}
	}

	// Do send QUIT messages back to the sender (who, by now, is not in the
	// channel anymore).
	result[sessionid.Id] = true
	if s.Server {
		// In order to reliably deliver the QUIT message to the affected
		// session, we need to iterate over all sessions since DeleteSession
		// removes the nick from all mappings.
		for id, session := range i.sessions {
			if session.deleted && NickToLower(session.Nick) == NickToLower(msg.Prefix.Name) {
				result[id.Id] = true
				break
			}
		}
	}

	return result
}

func (i *IRCServer) cmdQuit(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message

	i.DeleteSession(s)
	if s.loggedIn() {
		replies = append(replies, &irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  irc.QUIT,
			Trailing: msg.Trailing,
		})
	}

	return replies
}

func (i *IRCServer) interestPrivmsg(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	channel, ok := i.channels[ChanToLower(msg.Params[0])]
	if !ok {
		// It MUST either be a channel or a nick, otherwise no PRIVMSG reply is
		// generated. Hence no error checking.
		s, _ := i.nicks[NickToLower(msg.Params[0])]
		result[s.Id.Id] = true
		return result
	}

	prefixnick := NickToLower(msg.Prefix.Name)

	for nick, _ := range channel.nicks {
		session := i.nicks[nick].Id
		// Senders do not see their own messages.
		if nick == prefixnick {
			continue
		}
		result[session.Id] = true
	}

	return result
}

func (i *IRCServer) cmdPrivmsg(s *Session, msg *irc.Message) []*irc.Message {
	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NORECIPIENT,
			Params:   []string{s.Nick},
			Trailing: "No recipient given (PRIVMSG)",
		}}
	}

	if msg.Trailing == "" {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTEXTTOSEND,
			Params:   []string{s.Nick},
			Trailing: "No text to send",
		}}
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		if _, ok := i.channels[ChanToLower(msg.Params[0])]; !ok {
			return []*irc.Message{&irc.Message{
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, msg.Params[0]},
				Trailing: "No such channel",
			}}
		}
		return []*irc.Message{&irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  msg.Command,
			Params:   []string{msg.Params[0]},
			Trailing: msg.Trailing,
		}}
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	var replies []*irc.Message

	replies = append(replies, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  msg.Command,
		Params:   []string{msg.Params[0]},
		Trailing: msg.Trailing,
	})

	if session.AwayMsg != "" && msg.Command == irc.PRIVMSG {
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_AWAY,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: session.AwayMsg,
		})
	}

	return replies
}

func (i *IRCServer) interestMode(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	// Don’t send messages from services back to services.
	if msg.Prefix.Host != "services" {
		for _, serverid := range i.serverSessions {
			result[serverid] = true
		}
	}

	channel, ok := i.channels[ChanToLower(msg.Params[0])]
	if !ok {
		// It MUST either be a channel or a nick, otherwise no PRIVMSG reply is
		// generated. Hence no error checking.
		s, _ := i.nicks[NickToLower(msg.Params[0])]
		result[s.Id.Id] = true
		return result
	}

	for nick, _ := range channel.nicks {
		result[i.nicks[nick].Id.Id] = true
	}

	return result
}

func (i *IRCServer) cmdMode(s *Session, msg *irc.Message) []*irc.Message {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	if s.Channels[ChanToLower(channelname)] {
		// Channel must exist, the user is in it.
		c := i.channels[ChanToLower(channelname)]
		var modestr string
		if len(msg.Params) > 1 {
			modestr = msg.Params[1]
		}
		// TODO(secure): this is special cased for now. The behavior in
		// UnrealIRCD is to silently ignore query-modes (like b) when combined
		// with any other mode, even if it’s another query mode (like e).
		if modestr == "+b" {
			return []*irc.Message{&irc.Message{
				Command:  irc.RPL_ENDOFBANLIST,
				Params:   []string{s.Nick, channelname},
				Trailing: "End of Channel Ban List",
			}}
		}
		if strings.HasPrefix(modestr, "+") || strings.HasPrefix(modestr, "-") {
			if !c.nicks[NickToLower(s.Nick)][chanop] && !s.Operator {
				return []*irc.Message{&irc.Message{
					Command:  irc.ERR_CHANOPRIVSNEEDED,
					Params:   []string{s.Nick, channelname},
					Trailing: "You're not channel operator",
				}}
			}
			var replies []*irc.Message
			// true for adding a mode, false for removing it
			newvalue := strings.HasPrefix(modestr, "+")
			modearg := 2
			for _, char := range modestr[1:] {
				switch char {
				case '+', '-':
					newvalue = (char == '+')
				case 't', 's', 'i':
					c.modes[char] = newvalue
				case 'o':
					if len(msg.Params) > modearg {
						nick := msg.Params[modearg]
						perms, ok := c.nicks[NickToLower(nick)]
						if !ok {
							replies = append(replies, &irc.Message{
								Command:  irc.ERR_USERNOTINCHANNEL,
								Params:   []string{s.Nick, nick, channelname},
								Trailing: "They aren't on that channel",
							})
						} else {
							// If the user already is a chanop, silently do
							// nothing (like UnrealIRCd).
							if perms[chanop] != newvalue {
								c.nicks[NickToLower(nick)][chanop] = newvalue
							}
						}
					}
					modearg++
				default:
					replies = append(replies, &irc.Message{
						Command:  irc.ERR_UNKNOWNMODE,
						Params:   []string{s.Nick, string(char)},
						Trailing: "is unknown mode char to me",
					})
				}
			}
			if len(replies) > 0 {
				// TODO(secure): see how other ircds are handling this. do they sanity check the entire mode string before applying it, or do they keep valid modes while erroring for others?
				return replies
			}
			replies = append(replies, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: irc.MODE,
				Params:  msg.Params[:modearg],
			})
			return replies
		}
		if len(msg.Params) > 1 && msg.Params[1] == "b" {
			return []*irc.Message{&irc.Message{
				Command:  irc.RPL_ENDOFBANLIST,
				Params:   []string{s.Nick, channelname},
				Trailing: "End of Channel Ban List",
			}}
		} else {
			modestr := "+"
			for mode := 'A'; mode < 'z'; mode++ {
				if c.modes[mode] {
					modestr += string(mode)
				}
			}
			return []*irc.Message{&irc.Message{
				Command: irc.RPL_CHANNELMODEIS,
				Params:  []string{s.Nick, channelname, modestr},
			}}
		}
	} else {
		if NickToLower(channelname) == NickToLower(s.Nick) {
			modestr := "+"
			for mode := 'A'; mode < 'z'; mode++ {
				if s.modes[mode] {
					modestr += string(mode)
				}
			}
			return []*irc.Message{&irc.Message{
				Prefix:   &s.ircPrefix,
				Command:  irc.MODE,
				Params:   []string{s.Nick},
				Trailing: modestr,
			}}
		} else {
			return []*irc.Message{&irc.Message{
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "You're not on that channel",
			}}
		}
	}
}

func (i *IRCServer) cmdWho(s *Session, msg *irc.Message) []*irc.Message {
	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.RPL_ENDOFWHO,
			Params:   []string{s.Nick},
			Trailing: "End of /WHO list",
		}}
	}

	var replies []*irc.Message

	channelname := msg.Params[0]

	lastmsg := &irc.Message{
		Command:  irc.RPL_ENDOFWHO,
		Params:   []string{s.Nick, channelname},
		Trailing: "End of /WHO list",
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{lastmsg}
	}

	if c.modes['s'] {
		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			return []*irc.Message{lastmsg}
		}
	}

	nicks := make([]string, 0, len(c.nicks))
	for nick, _ := range c.nicks {
		nicks = append(nicks, i.nicks[nick].Nick)
	}

	sort.Strings(nicks)

	for _, nick := range nicks {
		session := i.nicks[NickToLower(nick)]
		prefix := session.ircPrefix
		goneStatus := "H"
		if session.AwayMsg != "" {
			goneStatus = "G"
		}
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_WHOREPLY,
			Params:   []string{s.Nick, channelname, prefix.User, prefix.Host, i.ServerPrefix.Name, prefix.Name, goneStatus},
			Trailing: "0 " + session.Realname,
		})
	}

	return append(replies, lastmsg)
}

func (i *IRCServer) cmdOper(s *Session, msg *irc.Message) []*irc.Message {
	name := msg.Params[0]
	password := msg.Params[1]
	authenticated := false
	for _, op := range i.Config.Operators {
		if op.Name == name && op.Password == password {
			authenticated = true
			break
		}
	}

	if !authenticated {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_PASSWDMISMATCH,
			Params:   []string{s.Nick},
			Trailing: "Password incorrect",
		}}
	}

	s.Operator = true
	s.modes['o'] = true

	modestr := "+"
	for mode := 'A'; mode < 'z'; mode++ {
		if s.modes[mode] {
			modestr += string(mode)
		}
	}

	return []*irc.Message{
		&irc.Message{
			Command:  irc.RPL_YOUREOPER,
			Params:   []string{s.Nick},
			Trailing: "You are now an IRC operator",
		},
		&irc.Message{
			Prefix:   msg.Prefix,
			Command:  irc.MODE,
			Params:   []string{s.Nick},
			Trailing: modestr,
		},
	}
}

func (i *IRCServer) cmdKill(s *Session, msg *irc.Message) []*irc.Message {
	if strings.TrimSpace(msg.Trailing) == "" {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, msg.Command},
			Trailing: "Not enough parameters",
		}}
	}

	if !s.Operator {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOPRIVILEGES,
			Params:   []string{s.Nick},
			Trailing: "Permission Denied - You're not an IRC operator",
		}}
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	prefix := session.ircPrefix
	i.DeleteSession(session)
	return []*irc.Message{&irc.Message{
		Prefix:   &prefix,
		Command:  irc.QUIT,
		Trailing: "Killed by " + s.Nick + ": " + msg.Trailing,
	}}
}

func (i *IRCServer) cmdAway(s *Session, msg *irc.Message) []*irc.Message {
	s.AwayMsg = strings.TrimSpace(msg.Trailing)
	if s.AwayMsg != "" {
		return []*irc.Message{&irc.Message{
			Command:  irc.RPL_NOWAWAY,
			Params:   []string{s.Nick},
			Trailing: "You have been marked as being away",
		}}
	} else {
		return []*irc.Message{&irc.Message{
			Command:  irc.RPL_UNAWAY,
			Params:   []string{s.Nick},
			Trailing: "You are no longer marked as being away",
		}}
	}
}

func relevantTopic(msg *irc.Message, prev, next logCursor) (bool, error) {
	if len(msg.Params) < 1 {
		return false, nil
	}

	for {
		nmsg, err := next()
		if err != nil {
			if err == CursorEOF {
				break
			}
			return true, err
		}
		// There is a newer TOPIC command for this channel, discard the old one.
		if nmsg.Command == irc.TOPIC && nmsg.Params[0] == msg.Params[0] {
			return false, nil
		}
	}

	return true, nil
}

func (i *IRCServer) interestTopic(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	// everyone who is in the channel whose topic was changed
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	// Send outgoing server-to-server messages only to servers.
	if len(msg.Params) > 1 {
		return result
	}

	channel := i.channels[ChanToLower(msg.Params[0])]
	for nick, _ := range channel.nicks {
		result[i.nicks[nick].Id.Id] = true
	}

	return result
}

func (i *IRCServer) cmdTopic(s *Session, msg *irc.Message) []*irc.Message {
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channel},
			Trailing: "No such channel",
		}}
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing == "" && msg.EmptyTrailing {
		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""

		return []*irc.Message{
			&irc.Message{
				Prefix:        &s.ircPrefix,
				Command:       irc.TOPIC,
				Params:        []string{channel},
				Trailing:      msg.Trailing,
				EmptyTrailing: true,
			},
			&irc.Message{
				Prefix:        &irc.Prefix{Name: s.Nick},
				Command:       irc.TOPIC,
				Params:        []string{channel, s.Nick, "0"},
				Trailing:      msg.Trailing,
				EmptyTrailing: true,
			},
		}
	}

	if !s.Channels[ChanToLower(channel)] {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, channel},
			Trailing: "You're not on that channel",
		}}
	}

	// “TOPIC”, i.e. get the topic.
	if msg.Trailing == "" {
		if c.topicTime.IsZero() {
			return []*irc.Message{&irc.Message{
				Command:  irc.RPL_NOTOPIC,
				Params:   []string{s.Nick, channel},
				Trailing: "No topic is set",
			}}
		}

		// TODO(secure): if the channel is secret, return ERR_NOTONCHANNEL

		return []*irc.Message{
			&irc.Message{
				Command:  irc.RPL_TOPIC,
				Params:   []string{s.Nick, channel},
				Trailing: c.topic,
			},
			&irc.Message{
				// RPL_TOPICWHOTIME (ircu-specific, not in the RFC)
				Command: "333",
				Params:  []string{s.Nick, channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10)},
			},
		}
	}

	if c.modes['t'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, channel},
			Trailing: "You're not channel operator",
		}}
	}

	c.topicNick = s.Nick
	c.topicTime = s.LastActivity
	c.topic = msg.Trailing

	return []*irc.Message{
		&irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  irc.TOPIC,
			Params:   []string{channel},
			Trailing: msg.Trailing,
		},
		&irc.Message{
			Prefix:   &irc.Prefix{Name: s.Nick},
			Command:  irc.TOPIC,
			Params:   []string{channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10)},
			Trailing: msg.Trailing,
		},
	}
}

func (i *IRCServer) cmdMotd(s *Session, msg *irc.Message) []*irc.Message {
	return []*irc.Message{
		&irc.Message{
			Command:  irc.RPL_MOTDSTART,
			Params:   []string{s.Nick},
			Trailing: "- " + i.ServerPrefix.Name + " Message of the day -",
		},
		// TODO(secure): make motd configurable
		&irc.Message{
			Command:  irc.RPL_MOTD,
			Params:   []string{s.Nick},
			Trailing: "- No MOTD configured yet.",
		},
		&irc.Message{
			Command:  irc.RPL_ENDOFMOTD,
			Params:   []string{s.Nick},
			Trailing: "End of MOTD command",
		},
	}
}

func (i *IRCServer) cmdPass(s *Session, msg *irc.Message) []*irc.Message {
	s.Pass = msg.Trailing
	return []*irc.Message{}
}

func (i *IRCServer) cmdWhois(s *Session, msg *irc.Message) []*irc.Message {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	var replies []*irc.Message

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_WHOISUSER,
		Params:   []string{s.Nick, session.Nick, session.ircPrefix.User, session.ircPrefix.Host, "*"},
		Trailing: session.Realname,
	})

	var channels []string
	for channel, _ := range session.Channels {
		var prefix string
		c := i.channels[channel]
		if c.modes['s'] && !s.Operator && !s.Channels[channel] {
			continue
		}
		if c.nicks[NickToLower(session.Nick)][chanop] {
			prefix = "@"
		}
		channels = append(channels, prefix+c.name)
	}

	sort.Strings(channels)

	if len(channels) > 0 {
		// TODO(secure): this needs to be split into multiple messages if the line exceeds 510 bytes.
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_WHOISCHANNELS,
			Params:   []string{s.Nick, session.Nick},
			Trailing: strings.Join(channels, " "),
		})
	}

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_WHOISSERVER,
		Params:   []string{s.Nick, session.Nick, i.ServerPrefix.Name},
		Trailing: "RobustIRC",
	})

	if session.Operator {
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_WHOISOPERATOR,
			Params:   []string{s.Nick, session.Nick},
			Trailing: "is an IRC operator",
		})
	}

	if session.AwayMsg != "" {
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_AWAY,
			Params:   []string{s.Nick, session.Nick},
			Trailing: session.AwayMsg,
		})
	}

	idle := strconv.FormatInt(int64(s.LastActivity.Sub(session.LastActivity).Seconds()), 10)
	signon := strconv.FormatInt(time.Unix(0, session.Id.Id).Unix(), 10)
	replies = append(replies, &irc.Message{
		Command:  irc.RPL_WHOISIDLE,
		Params:   []string{s.Nick, session.Nick, idle, signon},
		Trailing: "seconds idle, signon time",
	})

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_ENDOFWHOIS,
		Params:   []string{s.Nick, session.Nick},
		Trailing: "End of /WHOIS list",
	})

	return replies
}

func (i *IRCServer) cmdList(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message

	channels := make([]string, 0, len(i.channels))
	if len(msg.Params) > 0 {
		for _, channel := range strings.Split(msg.Params[0], ",") {
			channelname := ChanToLower(strings.TrimSpace(channel))
			if _, ok := i.channels[channelname]; ok {
				channels = append(channels, string(channelname))
			}
		}
	} else {
		for channel, _ := range i.channels {
			channels = append(channels, string(channel))
		}
		sort.Strings(channels)
	}
	for _, channel := range channels {
		c := i.channels[lcChan(channel)]
		if c.modes['s'] && !s.Operator && !s.Channels[lcChan(channel)] {
			continue
		}
		replies = append(replies, &irc.Message{
			Command:       irc.RPL_LIST,
			Params:        []string{s.Nick, c.name, strconv.Itoa(len(c.nicks))},
			Trailing:      c.topic,
			EmptyTrailing: c.topic == "",
		})
	}

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_LISTEND,
		Params:   []string{s.Nick},
		Trailing: "End of LIST",
	})
	return replies
}

func (i *IRCServer) interestInvite(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	result[i.nicks[NickToLower(msg.Params[0])].Id.Id] = true

	return result
}

func (i *IRCServer) cmdInvite(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message
	nickname := msg.Params[0]
	channelname := msg.Params[1]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, msg.Params[1]},
			Trailing: "You're not on that channel",
		}}
	}
	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}
	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_USERONCHANNEL,
			Params:   []string{s.Nick, session.Nick, c.name},
			Trailing: "is already on channel",
		}}
	}
	if c.modes['i'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, c.name},
			Trailing: "You're not channel operator",
		}}
	}
	session.invitedTo[ChanToLower(channelname)] = true
	replies = append(replies, &irc.Message{
		Command: irc.RPL_INVITING,
		Params:  []string{s.Nick, session.Nick, c.name},
	})
	replies = append(replies, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.INVITE,
		Params:   []string{session.Nick},
		Trailing: c.name,
	})
	replies = append(replies, &irc.Message{
		Command:  irc.NOTICE,
		Params:   []string{c.name},
		Trailing: fmt.Sprintf("%s invited %s into the channel.", s.Nick, msg.Params[0]),
	})

	if session.AwayMsg != "" {
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_AWAY,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: session.AwayMsg,
		})
	}

	return replies
}

func (i *IRCServer) cmdUserhost(s *Session, msg *irc.Message) []*irc.Message {
	var userhosts []string
	for _, nickname := range msg.Params {
		session, ok := i.nicks[NickToLower(nickname)]
		if !ok {
			continue
		}
		awayPrefix := "+"
		if session.AwayMsg != "" {
			awayPrefix = "-"
		}
		nick := session.Nick
		if session.Operator {
			nick = nick + "*"
		}
		userhosts = append(userhosts, fmt.Sprintf("%s=%s%s", nick, awayPrefix, session.ircPrefix.String()))
	}
	return []*irc.Message{&irc.Message{
		Command:       irc.RPL_USERHOST,
		Params:        []string{s.Nick},
		Trailing:      strings.Join(userhosts, " "),
		EmptyTrailing: len(userhosts) == 0,
	}}
}
