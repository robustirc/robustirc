package ircserver

import (
	"strconv"
	"strings"
	"time"

	"github.com/sorcix/irc"
)

// Each handler function must have the “cmd” prefix and return either a single
// *irc.Message or a slice of *irc.Message.

func init() {
	// Keep this list ordered the same way the functions below are ordered.
	commands["PING"] = &ircCommand{Func: cmdPing}
	commands["NICK"] = &ircCommand{
		Func: cmdNick,
		Interesting: func(s *Session, msg *irc.Message) bool {
			// TODO(secure): does it make sense to restrict this to Sessions which
			// have a channel in common? noting this because it doesn’t handle the
			// query-only use-case. if there’s no downside (except for the privacy
			// aspect), perhaps leave it as-is?
			return true
		},
	}
	commands["USER"] = &ircCommand{Func: cmdUser, MinParams: 3}
	commands["JOIN"] = &ircCommand{
		Func:        cmdJoin,
		MinParams:   1,
		Interesting: interestJoin,
	}
	commands["PART"] = &ircCommand{
		Func:        cmdPart,
		MinParams:   1,
		Interesting: interestPart,
	}
	commands["QUIT"] = &ircCommand{
		Func: cmdQuit,
		Interesting: func(s *Session, msg *irc.Message) bool {
			// TODO(secure): does it make sense to restrict this to Sessions which
			// have a channel in common? noting this because it doesn’t handle the
			// query-only use-case. if there’s no downside (except for the privacy
			// aspect), perhaps leave it as-is?
			return true
		},
	}
	commands["PRIVMSG"] = &ircCommand{Func: cmdPrivmsg, Interesting: interestPrivmsg}
	commands["MODE"] = &ircCommand{
		Func:        cmdMode,
		MinParams:   1,
		Interesting: commonChannelOrDirect,
	}
	commands["WHO"] = &ircCommand{Func: cmdWho}
	commands["OPER"] = &ircCommand{Func: cmdOper, MinParams: 2}
	commands["KILL"] = &ircCommand{Func: cmdKill, MinParams: 1}
	commands["AWAY"] = &ircCommand{Func: cmdAway}
	commands["TOPIC"] = &ircCommand{
		Func:        cmdTopic,
		MinParams:   1,
		Interesting: interestTopic,
	}
	commands["MOTD"] = &ircCommand{Func: cmdMotd}
}

// commonChannelOrDirect returns true when msg’s first parameter is a channel
// name of 's' or when the first parameter is the nickname of 's'.
func commonChannelOrDirect(s *Session, msg *irc.Message) bool {
	return s.Channels[msg.Params[0]] || msg.Params[0] == s.Nick
}

func cmdPing(s *Session, msg *irc.Message) []*irc.Message {
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

func cmdNick(s *Session, msg *irc.Message) []*irc.Message {
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
			Trailing: "Erroneus nickname.",
		}}
	}

	// TODO(secure): add a map for quick nickname lookup.
	for _, session := range Sessions {
		if NickToLower(session.Nick) == NickToLower(msg.Params[0]) {
			return []*irc.Message{&irc.Message{
				Command:  irc.ERR_NICKNAMEINUSE,
				Params:   []string{"*", msg.Params[0]},
				Trailing: "Nickname is already in use.",
			}}
		}
	}
	s.Nick = msg.Params[0]
	s.updateIrcPrefix()
	if oldPrefix.String() != "" {
		return []*irc.Message{&irc.Message{
			Prefix:   &oldPrefix,
			Command:  irc.NICK,
			Trailing: msg.Params[0],
		}}
	}

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
		Trailing: "Your host is " + ServerPrefix.Name,
	})

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_CREATED,
		Params:   []string{s.Nick},
		Trailing: "This server was created " + serverCreation.String(),
	})

	replies = append(replies, &irc.Message{
		Command: irc.RPL_MYINFO,
		Params:  []string{s.Nick},
		// TODO(secure): actually support these modes.
		Trailing: ServerPrefix.Name + " v1 i nst",
	})

	// send ISUPPORT as per http://www.irc.org/tech_docs/draft-brocklesby-irc-isupport-03.txt
	replies = append(replies, &irc.Message{
		Command: "005",
		Params: []string{
			"CHANTYPES=#",
			"CHANNELLEN=" + maxChannelLen,
			"NICKLEN=" + maxNickLen,
			"MODES=1",
			"PREFIX=",
		},
		Trailing: "are supported by this server",
	})

	replies = append(replies, cmdMotd(s, msg)...)

	return replies
}

func cmdUser(s *Session, msg *irc.Message) []*irc.Message {
	// We keep the username (so that bans are more effective) and realname
	// (some people actually set it and look at it).
	s.Username = msg.Params[0]
	s.Realname = msg.Trailing
	s.updateIrcPrefix()
	return []*irc.Message{}
}

func interestJoin(s *Session, msg *irc.Message) bool {
	return s.Channels[msg.Trailing]
}

func cmdJoin(s *Session, msg *irc.Message) []*irc.Message {
	// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
	channelname := msg.Params[0]
	if !IsValidChannel(channelname) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "No such channel",
		}}
	}
	c, ok := channels[channelname]
	if !ok {
		c = &channel{
			nicks: make(map[string]*[maxChanMemberStatus]bool),
		}
		channels[channelname] = c
	}
	c.nicks[s.Nick] = &[maxChanMemberStatus]bool{}
	// If the channel did not exist before, the first joining user becomes a
	// channel operator.
	if !ok {
		c.nicks[s.Nick][chanop] = true
	}
	s.Channels[channelname] = true

	nicks := make([]string, 0, len(c.nicks))
	for nick := range c.nicks {
		nicks = append(nicks, nick)
	}

	var replies []*irc.Message

	replies = append(replies, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.JOIN,
		Trailing: channelname,
	})
	// Integrate the topic response by simulating a TOPIC command.
	replies = append(replies, cmdTopic(s, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})...)
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

	return replies
}

func interestPart(s *Session, msg *irc.Message) bool {
	// Do send PART messages back to the sender (who, by now, is not in the
	// channel anymore).
	return s.ircPrefix == *msg.Prefix || s.Channels[msg.Params[0]]
}

func cmdPart(s *Session, msg *irc.Message) []*irc.Message {
	// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
	channelname := msg.Params[0]

	c, ok := channels[channelname]
	if !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "No such channel",
		}}
	}

	if _, ok := c.nicks[s.Nick]; !ok {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "You're not on that channel",
		}}
	}

	delete(c.nicks, s.Nick)
	if len(c.nicks) == 0 {
		delete(channels, channelname)
	}
	delete(s.Channels, channelname)
	return []*irc.Message{&irc.Message{
		Prefix:  &s.ircPrefix,
		Command: irc.PART,
		Params:  []string{channelname},
	}}
}

func cmdQuit(s *Session, msg *irc.Message) []*irc.Message {
	return []*irc.Message{&irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.QUIT,
		Trailing: msg.Trailing,
	}}
}

func interestPrivmsg(s *Session, msg *irc.Message) bool {
	// Don’t send messages back to the sender.
	if s.ircPrefix == *msg.Prefix {
		return false
	}

	return commonChannelOrDirect(s, msg)
}

func cmdPrivmsg(s *Session, msg *irc.Message) []*irc.Message {
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
			Command:  irc.PRIVMSG,
			Params:   []string{msg.Params[0]},
			Trailing: msg.Trailing,
		}}
	}

	// TODO(secure): add a map for quick nickname lookup.
	for _, session := range Sessions {
		if NickToLower(session.Nick) == NickToLower(msg.Params[0]) {
			var replies []*irc.Message

			replies = append(replies, &irc.Message{
				Prefix:   &s.ircPrefix,
				Command:  irc.PRIVMSG,
				Params:   []string{msg.Params[0]},
				Trailing: msg.Trailing,
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
	}

	return []*irc.Message{&irc.Message{
		Command:  irc.ERR_NOSUCHNICK,
		Params:   []string{s.Nick, msg.Params[0]},
		Trailing: "No such nick/channel",
	}}
}

func cmdMode(s *Session, msg *irc.Message) []*irc.Message {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	if s.Channels[channelname] {
		// Channel must exist, the user is in it.
		c := channels[channelname]
		var modestr string
		if len(msg.Params) > 1 {
			modestr = msg.Params[1]
		}
		if strings.HasPrefix(modestr, "+") || strings.HasPrefix(modestr, "-") {
			if !c.nicks[s.Nick][chanop] {
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
				case 't':
					c.modes[char] = newvalue
				case 'o':
					if len(msg.Params) > modearg {
						nick := msg.Params[modearg]
						perms, ok := c.nicks[nick]
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
								c.nicks[nick][chanop] = newvalue
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
		if channelname == s.Nick {
			return []*irc.Message{&irc.Message{
				Prefix:   &s.ircPrefix,
				Command:  irc.MODE,
				Params:   []string{s.Nick},
				Trailing: "+",
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

func cmdWho(s *Session, msg *irc.Message) []*irc.Message {
	if len(msg.Params) < 1 {
		return []*irc.Message{&irc.Message{
			Command:  irc.RPL_ENDOFWHO,
			Params:   []string{s.Nick},
			Trailing: "End of /WHO list",
		}}
	}

	var replies []*irc.Message

	channel := msg.Params[0]
	// TODO(secure): a separate map for quick lookup may be worthwhile for big channels.
	for _, session := range Sessions {
		if !session.Channels[channel] {
			continue
		}
		prefix := session.ircPrefix
		replies = append(replies, &irc.Message{
			Command:  irc.RPL_WHOREPLY,
			Params:   []string{s.Nick, channel, prefix.User, prefix.Host, ServerPrefix.Name, prefix.Name, "H"},
			Trailing: "0 Unknown",
		})
	}

	replies = append(replies, &irc.Message{
		Command:  irc.RPL_ENDOFWHO,
		Params:   []string{s.Nick, channel},
		Trailing: "End of /WHO list",
	})

	return replies
}

func cmdOper(s *Session, msg *irc.Message) []*irc.Message {
	// TODO(secure): implement restriction to certain hosts once we have a
	// configuration file. (ERR_NOOPERHOST)

	if msg.Params[1] != NetworkPassword {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_PASSWDMISMATCH,
			Params:   []string{s.Nick},
			Trailing: "Password incorrect",
		}}
	}

	s.Operator = true

	return []*irc.Message{&irc.Message{
		Command:  irc.RPL_YOUREOPER,
		Params:   []string{s.Nick},
		Trailing: "You are now an IRC operator",
	}}
}

func cmdKill(s *Session, msg *irc.Message) []*irc.Message {
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

	for _, session := range Sessions {
		if NickToLower(session.Nick) != NickToLower(msg.Params[0]) {
			continue
		}

		prefix := session.ircPrefix
		DeleteSession(session.Id)
		return []*irc.Message{&irc.Message{
			Prefix:   &prefix,
			Command:  irc.QUIT,
			Trailing: "Killed by " + s.Nick + ": " + msg.Trailing,
		}}
	}

	return []*irc.Message{&irc.Message{
		Command:  irc.ERR_NOSUCHNICK,
		Params:   []string{s.Nick, msg.Params[0]},
		Trailing: "No such nick/channel",
	}}
}

func cmdAway(s *Session, msg *irc.Message) []*irc.Message {
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

func interestTopic(s *Session, msg *irc.Message) bool {
	return s.Channels[msg.Params[0]]
}

func cmdTopic(s *Session, msg *irc.Message) []*irc.Message {
	channel := msg.Params[0]
	c, ok := channels[channel]
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

		return []*irc.Message{&irc.Message{
			Prefix:        &s.ircPrefix,
			Command:       irc.TOPIC,
			Params:        []string{channel},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		}}
	}

	if !s.Channels[channel] {
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

	if c.modes['t'] && !c.nicks[s.Nick][chanop] {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, channel},
			Trailing: "You're not channel operator",
		}}
	}

	c.topicNick = s.Nick
	c.topicTime = time.Now()
	c.topic = msg.Trailing

	return []*irc.Message{&irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.TOPIC,
		Params:   []string{channel},
		Trailing: msg.Trailing,
	}}
}

func cmdMotd(s *Session, msg *irc.Message) []*irc.Message {
	return []*irc.Message{
		&irc.Message{
			Command:  irc.RPL_MOTDSTART,
			Params:   []string{s.Nick},
			Trailing: "- " + ServerPrefix.Name + " Message of the day - ",
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
