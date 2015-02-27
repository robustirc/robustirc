package ircserver

import (
	"fmt"
	"hash/fnv"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

func init() {
	// NB: This command doesn’t have the server_ prefix because it is sent in
	// order to make a session _become_ a server. Having the function in this
	// file makes more sense than in commands.go.
	commands["SERVER"] = &ircCommand{Func: (*IRCServer).cmdServer, MinParams: 2}

	commands["SJOIN"] = &ircCommand{Func: (*IRCServer).ignoreCmd, Interesting: (*IRCServer).interestSjoin}

	// These just use exactly the same code as clients. We can directly assign
	// the contents of commands[x] because commands.go is sorted lexically
	// before server_commands.go. For details, see
	// http://golang.org/ref/spec#Package_initialization.
	commands["server_PING"] = commands["PING"]
	commands["server_QUIT"] = commands["QUIT"]

	commands["server_NICK"] = &ircCommand{Func: (*IRCServer).cmdServerNick}
	commands["server_MODE"] = &ircCommand{Func: (*IRCServer).cmdServerMode}
	commands["server_JOIN"] = &ircCommand{Func: (*IRCServer).cmdServerJoin}
	commands["server_PRIVMSG"] = &ircCommand{Func: (*IRCServer).cmdServerPrivmsg}
	commands["server_NOTICE"] = &ircCommand{Func: (*IRCServer).cmdServerPrivmsg}
	commands["server_TOPIC"] = &ircCommand{Func: (*IRCServer).cmdServerTopic, MinParams: 3}
	commands["server_SVSNICK"] = &ircCommand{Func: (*IRCServer).cmdServerSvsnick, MinParams: 2}
	commands["server_KILL"] = &ircCommand{Func: (*IRCServer).cmdServerKill, MinParams: 1}
	commands["server_KICK"] = &ircCommand{Func: (*IRCServer).cmdServerKick, MinParams: 2}
	// TODO: add server_SVSMODE which is for changing the user mode
	// TODO: operserv is not yet usable (access denied)
}

func (i *IRCServer) ignoreCmd(s *Session, msg *irc.Message) []*irc.Message {
	return []*irc.Message{}
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
	if len(c.nicks) == 0 {
		delete(i.channels, ChanToLower(channelname))
	}
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
			Params:   []string{s.Nick, msg.Params[0]},
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

func (i *IRCServer) cmdServerNick(s *Session, msg *irc.Message) []*irc.Message {
	log.Printf("got nick: %+v\n", msg)
	// Could be either a nickchange or the introduction of a new user.
	if len(msg.Params) == 1 {
		// TODO(secure): handle nickchanges. not sure when/if those are used. botserv maybe?
		return []*irc.Message{}
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
	// TODO(secure): handle nick collisions. best to forbid *Serv nicks in cmdNick
	i.nicks[NickToLower(ss.Nick)] = ss
	ss.Username = msg.Params[3]
	ss.updateIrcPrefix()
	return []*irc.Message{}

	// <nickname> <hopcount> <username> <host> <servertoken> <umode> <realname>
	// OperServ 1 1422134861 services localhost.net services.localhost.net 0 :Operator Server
}

func (i *IRCServer) cmdServerMode(s *Session, msg *irc.Message) []*irc.Message {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		// TODO
		return []*irc.Message{}
	}

	// TODO(secure): possibly refactor this with cmdMode()
	var modestr string
	if len(msg.Params) > 1 {
		modestr = msg.Params[1]
	}
	if strings.HasPrefix(modestr, "+") || strings.HasPrefix(modestr, "-") {
		var replies []*irc.Message
		// true for adding a mode, false for removing it
		newvalue := strings.HasPrefix(modestr, "+")
		modearg := 2
		for _, char := range modestr[1:] {
			switch char {
			case '+', '-':
				newvalue = (char == '+')
			case 't', 's', 'r':
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
			return replies
		}
		replies = append(replies, &irc.Message{
			// TODO: refactor into a servicesprefix function or sth
			Prefix: &irc.Prefix{
				Name: msg.Prefix.Name,
				User: "services",
				Host: "services",
			},
			Command: irc.MODE,
			Params:  msg.Params[:modearg],
		})
		return replies
	}

	// TODO
	return []*irc.Message{}
}

func (i *IRCServer) cmdServer(s *Session, msg *irc.Message) []*irc.Message {
	// TODO(secure): make this configurable once we have a config file.
	if s.Pass != "services=mypass" {
		return []*irc.Message{&irc.Message{
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
			"0",  // hopcount
			"23", // token, must be different from the services token
		},
	}}
	for _, session := range i.sessions {
		// Skip sessions that are not yet logged in, sessions that represent a
		// server connection and subsessions of a server connection.
		if !session.loggedIn() || session.Server || session.Id.Reply != 0 {
			continue
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
				"0", // svid TODO: this field is not in the RFC?
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

func (i *IRCServer) cmdServerSvsnick(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “SVSNICK blArgh Guest30503 :1425036445”
	if !IsValidNickname(msg.Params[1]) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_ERRONEUSNICKNAME,
			Params:   []string{"*", msg.Params[1]},
			Trailing: "Erroneus nickname.",
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
	// TODO(secure): handle this
	return []*irc.Message{}
}

func (i *IRCServer) cmdServerTopic(s *Session, msg *irc.Message) []*irc.Message {
	// e.g. “:ChanServ TOPIC #chaos-hd ChanServ 0 :”
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{"*", channel},
			Trailing: "No such channel",
		}}
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing == "" && msg.EmptyTrailing {
		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""
		return []*irc.Message{&irc.Message{
			Prefix:        msg.Prefix,
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
		Prefix:   msg.Prefix,
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
		Prefix:   &irc.Prefix{Name: msg.Prefix.Name, User: "services", Host: "robust/TODO"},
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
