package ircserver

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

func init() {
	// When developing the anope RobustIRC module, I used the following command
	// to make sure all commands which anope sends are implemented:
	//
	// perl -nlE 'my ($cmd) = ($_ =~ /send_cmd\([^,]+, "([^" ]+)/); say $cmd if defined($cmd)' src/protocol/robustirc.c | sort | uniq

	// NB: This command doesn’t have the server_ prefix because it is sent in
	// order to make a session _become_ a server. Having the function in this
	// file makes more sense than in commands.go.
	commands["SERVER"] = &ircCommand{Func: (*IRCServer).cmdServer, MinParams: 2}

	commands["SJOIN"] = &ircCommand{Func: (*IRCServer).unknownCmd, Interesting: (*IRCServer).interestSjoin}

	// These just use exactly the same code as clients. We can directly assign
	// the contents of commands[x] because commands.go is sorted lexically
	// before server_commands.go. For details, see
	// http://golang.org/ref/spec#Package_initialization.
	commands["server_PING"] = commands["PING"]

	commands["server_QUIT"] = &ircCommand{Func: (*IRCServer).cmdServerQuit}
	commands["server_NICK"] = &ircCommand{Func: (*IRCServer).cmdServerNick}
	commands["server_MODE"] = &ircCommand{Func: (*IRCServer).cmdServerMode}
	commands["server_JOIN"] = &ircCommand{Func: (*IRCServer).cmdServerJoin}
	commands["server_PART"] = &ircCommand{Func: (*IRCServer).cmdServerPart}
	commands["server_PRIVMSG"] = &ircCommand{Func: (*IRCServer).cmdServerPrivmsg}
	commands["server_NOTICE"] = &ircCommand{Func: (*IRCServer).cmdServerPrivmsg}
	commands["server_TOPIC"] = &ircCommand{Func: (*IRCServer).cmdServerTopic, MinParams: 3}
	commands["server_SVSNICK"] = &ircCommand{Func: (*IRCServer).cmdServerSvsnick, MinParams: 2}
	commands["server_SVSMODE"] = &ircCommand{Func: (*IRCServer).cmdServerSvsmode, MinParams: 2}
	commands["server_KILL"] = &ircCommand{Func: (*IRCServer).cmdServerKill, MinParams: 1}
	commands["server_KICK"] = &ircCommand{Func: (*IRCServer).cmdServerKick, MinParams: 2}
	commands["server_INVITE"] = &ircCommand{Func: (*IRCServer).cmdServerInvite, MinParams: 2}
}

func servicesPrefix(prefix *irc.Prefix) *irc.Prefix {
	return &irc.Prefix{
		Name: prefix.Name,
		User: "services",
		Host: "services"}
}

func (i *IRCServer) unknownCmd(s *Session, msg *irc.Message) []*irc.Message {
	return []*irc.Message{&irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.ERR_UNKNOWNCOMMAND,
		Params:   []string{s.Nick, msg.Command},
		Trailing: "Unknown command",
	}}
}

func (i *IRCServer) interestSjoin(sessionid types.RobustId, msg *irc.Message) map[int64]bool {
	result := make(map[int64]bool)

	for _, serverid := range i.serverSessions {
		result[serverid] = true
	}

	return result
}

func (i *IRCServer) cmdServerKick(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “:ChanServ KICK #noname-ev blArgh_ :get out”
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{"*", channelname},
			Trailing: "No such nick/channel",
		}}
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_USERNOTINCHANNEL,
			Params:   []string{"*", msg.Params[1], channelname},
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
		Prefix: &irc.Prefix{
			Name: msg.Prefix.Name,
			User: "services",
			Host: "services",
		},
		Command:  irc.KICK,
		Params:   []string{msg.Params[0], msg.Params[1]},
		Trailing: msg.Trailing,
	}}
}

func (i *IRCServer) cmdServerKill(s *Session, msg *irc.Message) []*irc.Message {
	if strings.TrimSpace(msg.Trailing) == "" {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{"*", msg.Command},
			Trailing: "Not enough parameters",
		}}
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	i.DeleteSession(session)
	return []*irc.Message{&irc.Message{
		Prefix:   &session.ircPrefix,
		Command:  irc.QUIT,
		Trailing: "Killed: " + msg.Trailing,
	}}
}

func (i *IRCServer) cmdServerQuit(s *Session, msg *irc.Message) []*irc.Message {
	// No prefix means the server quits the entire session.
	if msg.Prefix == nil {
		i.DeleteSession(s)
		var replies []*irc.Message
		// For services, we also need to delete all sessions that share the
		// same .Id, but have a different .Reply.
		for id, session := range i.sessions {
			if id.Id != s.Id.Id || id.Reply == 0 {
				continue
			}
			i.DeleteSession(session)
			replies = append(replies, &irc.Message{
				Prefix:   &session.ircPrefix,
				Command:  irc.QUIT,
				Trailing: msg.Trailing,
			})
		}
		return replies
	}

	// We got a prefix, so only a single session quits (e.g. nickname
	// enforcer).
	for id, session := range i.sessions {
		if id.Id != s.Id.Id || id.Reply == 0 || NickToLower(session.Nick) != NickToLower(msg.Prefix.Name) {
			continue
		}
		i.DeleteSession(session)
		return []*irc.Message{&irc.Message{
			Prefix:   &session.ircPrefix,
			Command:  irc.QUIT,
			Trailing: msg.Trailing,
		}}
	}
	return []*irc.Message{}
}

func (i *IRCServer) cmdServerNick(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “NICK OperServ 1 1422134861 services localhost.net services.localhost.net 0 :Operator Server”
	// <nickname> <hopcount> <username> <host> <servertoken> <umode> <realname>

	// Could be either a nickchange or the introduction of a new user.
	if len(msg.Params) == 1 {
		// TODO(secure): handle nickchanges. not sure when/if those are used. botserv maybe?
		return []*irc.Message{}
	}

	if _, ok := i.nicks[NickToLower(msg.Params[0])]; ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NICKNAMEINUSE,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "Nickname is already in use",
		}}
	}

	h := fnv.New64()
	h.Write([]byte(msg.Params[0]))
	id := types.RobustId{
		Id:    s.Id.Id,
		Reply: int64(h.Sum64()),
	}

	i.CreateSession(id, "")
	ss, _ := i.GetSession(id)
	ss.Nick = msg.Params[0]
	i.nicks[NickToLower(ss.Nick)] = ss
	ss.Username = msg.Params[3]
	ss.Realname = msg.Trailing
	ss.updateIrcPrefix()
	return []*irc.Message{}
}

func (i *IRCServer) cmdServerMode(s *Session, msg *irc.Message) []*irc.Message {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	// TODO(secure): possibly refactor this with cmdMode()
	var modestr string
	if len(msg.Params) > 1 {
		modestr = msg.Params[1]
	}
	if !strings.HasPrefix(modestr, "+") && !strings.HasPrefix(modestr, "-") {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_UNKNOWNMODE,
			Params:   []string{msg.Prefix.Name, modestr},
			Trailing: "is unknown mode char to me",
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
		case 't', 's', 'r', 'i':
			c.modes[char] = newvalue
		case 'o':
			if len(msg.Params) > modearg {
				nick := msg.Params[modearg]
				perms, ok := c.nicks[NickToLower(nick)]
				if !ok {
					replies = append(replies, &irc.Message{
						Command:  irc.ERR_USERNOTINCHANNEL,
						Params:   []string{msg.Prefix.Name, nick, channelname},
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
				Params:   []string{msg.Prefix.Name, string(char)},
				Trailing: "is unknown mode char to me",
			})
		}
	}
	if len(replies) > 0 {
		return replies
	}
	replies = append(replies, &irc.Message{
		Prefix:  servicesPrefix(msg.Prefix),
		Command: irc.MODE,
		Params:  msg.Params[:modearg],
	})
	return replies
}

func (i *IRCServer) cmdServer(s *Session, msg *irc.Message) []*irc.Message {
	authenticated := false
	for _, service := range i.Config.Services {
		if s.Pass == "services="+service.Password {
			authenticated = true
			break
		}
	}
	if !authenticated {
		return []*irc.Message{&irc.Message{
			Prefix:   &irc.Prefix{},
			Command:  irc.ERROR,
			Trailing: "Invalid password",
		}}
	}
	s.Server = true
	s.ircPrefix = irc.Prefix{
		Name: msg.Params[0],
	}
	i.serverSessions = append(i.serverSessions, s.Id.Id)
	replies := []*irc.Message{&irc.Message{
		Prefix:  &irc.Prefix{},
		Command: "SERVER",
		Params: []string{
			i.ServerPrefix.Name,
			"1",  // hopcount
			"23", // token, must be different from the services token
		},
	}}
	nicks := make([]string, 0, len(i.nicks))
	for nick, _ := range i.nicks {
		nicks = append(nicks, string(nick))
	}
	sort.Strings(nicks)
	for _, nick := range nicks {
		session := i.nicks[lcNick(nick)]
		// Skip sessions that are not yet logged in, sessions that represent a
		// server connection and subsessions of a server connection.
		if !session.loggedIn() || session.Server || session.Id.Reply != 0 {
			continue
		}
		modestr := "+"
		for mode := 'A'; mode < 'z'; mode++ {
			if session.modes[mode] {
				modestr += string(mode)
			}
		}
		replies = append(replies, &irc.Message{
			Prefix:  &irc.Prefix{},
			Command: irc.NICK,
			Params: []string{
				session.Nick,
				"1", // hopcount (ignored by anope)
				"1", // timestamp
				session.Username,
				session.ircPrefix.Host,
				i.ServerPrefix.Name,
				"0", // svid, an identifier set by the services
				modestr,
			},
			Trailing: session.Realname,
		})
		for channelname := range session.Channels {
			var prefix string

			if i.channels[channelname].nicks[NickToLower(session.Nick)][chanop] {
				prefix = prefix + string('@')
			}
			replies = append(replies, &irc.Message{
				Command:  "SJOIN",
				Params:   []string{"1", i.channels[channelname].name},
				Trailing: prefix + session.Nick,
			})
		}
	}
	return replies
}

func (i *IRCServer) cmdServerSvsmode(s *Session, msg *irc.Message) []*irc.Message {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}
	modestr := msg.Params[1]
	if !strings.HasPrefix(modestr, "+") && !strings.HasPrefix(modestr, "-") {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_UMODEUNKNOWNFLAG,
			Params:   []string{"*"},
			Trailing: "Unknown MODE flag",
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
		case 'd':
			// This is used to set an arbitrary identifier by services. Anope
			// sets this to a timestamp, and e.g. UnrealIRCD doesn’t do
			// anything with it, so we just ignore it.
			modearg++
		case 'r':
			// Store registered flag
			session.modes[char] = newvalue
		default:
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_UMODEUNKNOWNFLAG,
				Params:   []string{"*"},
				Trailing: "Unknown MODE flag",
			})
		}
	}
	modestr = "+"
	for mode := 'A'; mode < 'z'; mode++ {
		if session.modes[mode] {
			modestr += string(mode)
		}
	}
	replies = append(replies, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.MODE,
		Params:   []string{session.Nick},
		Trailing: modestr,
	})
	return replies
}

func (i *IRCServer) cmdServerSvsnick(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “SVSNICK blArgh Guest30503 :1425036445”
	if !IsValidNickname(msg.Params[1]) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_ERRONEUSNICKNAME,
			Params:   []string{"*", msg.Params[1]},
			Trailing: "Erroneous nickname",
		}}
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	// TODO(secure): kill this code duplication with cmdNick()
	oldPrefix := session.ircPrefix
	oldNick := NickToLower(msg.Params[0])
	session.Nick = msg.Params[1]
	i.nicks[NickToLower(session.Nick)] = session
	delete(i.nicks, oldNick)
	for _, c := range i.channels {
		if modes, ok := c.nicks[oldNick]; ok {
			c.nicks[NickToLower(session.Nick)] = modes
		}
		delete(c.nicks, oldNick)
	}
	session.updateIrcPrefix()
	return []*irc.Message{&irc.Message{
		Prefix:   &oldPrefix,
		Command:  irc.NICK,
		Trailing: session.Nick,
	}}
}

func (i *IRCServer) cmdServerJoin(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “:ChanServ JOIN #noname-ev” (before enforcing AKICK).
	var replies []*irc.Message

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		if !IsValidChannel(channelname) {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "No such channel",
			})
			continue
		}
		// TODO(secure): reduce code duplication with cmdJoin()
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			i.channels[ChanToLower(channelname)] = c
		}
		c.nicks[NickToLower(msg.Prefix.Name)] = &[maxChanMemberStatus]bool{}
		// If the channel did not exist before, the first joining user becomes a
		// channel operator.
		if !ok {
			c.nicks[NickToLower(msg.Prefix.Name)][chanop] = true
		}
		s.Channels[ChanToLower(channelname)] = true

		replies = append(replies, &irc.Message{
			Prefix:   servicesPrefix(msg.Prefix),
			Command:  irc.JOIN,
			Trailing: channelname,
		})
	}
	return replies
}

func (i *IRCServer) cmdServerPart(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “:ChanServ PART #noname-ev” (after enforcing AKICK).
	var replies []*irc.Message

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "No such channel",
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(msg.Prefix.Name)]; !ok {
			replies = append(replies, &irc.Message{
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "You're not on that channel",
			})
			continue
		}

		// TODO(secure): reduce code duplication with cmdPart()
		delete(c.nicks, NickToLower(msg.Prefix.Name))
		i.maybeDeleteChannel(c)
		delete(s.Channels, ChanToLower(channelname))
		replies = append(replies, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: irc.PART,
			Params:  []string{channelname},
		})
	}

	return replies
}

func (i *IRCServer) cmdServerTopic(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “:ChanServ TOPIC #chaos-hd ChanServ 0 :”
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, channel},
			Trailing: "No such channel",
		}}
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing == "" && msg.EmptyTrailing {
		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""
		return []*irc.Message{&irc.Message{
			Prefix:        servicesPrefix(msg.Prefix),
			Command:       irc.TOPIC,
			Params:        []string{channel},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		}}
	}

	ts, err := strconv.ParseInt(msg.Params[2], 0, 64)
	if err != nil {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{"*", channel},
			Trailing: fmt.Sprintf("Could not parse timestamp: %v", err),
		}}
	}

	c.topicNick = msg.Params[1]
	c.topicTime = time.Unix(ts, 0)
	c.topic = msg.Trailing

	return []*irc.Message{&irc.Message{
		Prefix:   servicesPrefix(msg.Prefix),
		Command:  irc.TOPIC,
		Params:   []string{channel},
		Trailing: msg.Trailing,
	}}
}

// The only difference is that we re-use (and augment) the msg.Prefix instead of setting s.Prefix.
// TODO(secure): refactor this with cmdPrivmsg possibly?
func (i *IRCServer) cmdServerPrivmsg(s *Session, msg *irc.Message) []*irc.Message {
	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NORECIPIENT,
			Params:   []string{msg.Prefix.Name},
			Trailing: fmt.Sprintf("No recipient given (%s)", msg.Command),
		}}
	}

	if msg.Trailing == "" {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTEXTTOSEND,
			Params:   []string{msg.Prefix.Name},
			Trailing: "No text to send",
		}}
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		return []*irc.Message{&irc.Message{
			Prefix:   servicesPrefix(msg.Prefix),
			Command:  msg.Command,
			Params:   []string{msg.Params[0]},
			Trailing: msg.Trailing,
		}}
	}

	if _, ok := i.nicks[NickToLower(msg.Params[0])]; !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	var replies []*irc.Message

	replies = append(replies, &irc.Message{
		Prefix:   servicesPrefix(msg.Prefix),
		Command:  msg.Command,
		Params:   []string{msg.Params[0]},
		Trailing: msg.Trailing,
	})

	return replies
}

// TODO(secure): refactor this with cmdInvite possibly?
func (i *IRCServer) cmdServerInvite(s *Session, msg *irc.Message) []*irc.Message {
	var replies []*irc.Message
	nickname := msg.Params[0]
	channelname := msg.Params[1]

	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		}}
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, msg.Params[1]},
			Trailing: "No such channel",
		}}
	}

	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_USERONCHANNEL,
			Params:   []string{msg.Prefix.Name, session.Nick, c.name},
			Trailing: "is already on channel",
		}}
	}

	session.invitedTo[ChanToLower(channelname)] = true
	replies = append(replies, &irc.Message{
		Command: irc.RPL_INVITING,
		Params:  []string{msg.Prefix.Name, msg.Params[0], c.name},
	})
	replies = append(replies, &irc.Message{
		Prefix:   servicesPrefix(msg.Prefix),
		Command:  irc.INVITE,
		Params:   []string{session.Nick},
		Trailing: c.name,
	})
	replies = append(replies, &irc.Message{
		Command:  irc.NOTICE,
		Params:   []string{c.name},
		Trailing: fmt.Sprintf("%s invited %s into the channel.", msg.Prefix.Name, msg.Params[0]),
	})

	return replies
}
