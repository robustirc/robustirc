package ircserver

import (
	"strings"

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

	// TODO(secure): send 002, 003, 004, 251, 252, 254, 255, 265, 266, [motd = 375, 372, 376]
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

	return replies
}

func cmdUser(s *Session, msg *irc.Message) []*irc.Message {
	// We don’t need any information from the USER message.
	return []*irc.Message{}
}

func interestJoin(s *Session, msg *irc.Message) bool {
	return s.Channels[msg.Trailing]
}

func cmdJoin(s *Session, msg *irc.Message) []*irc.Message {
	// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
	channel := msg.Params[0]
	if !IsValidChannel(channel) {
		return []*irc.Message{&irc.Message{
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channel},
			Trailing: "No such channel",
		}}
	}
	s.Channels[channel] = true
	var nicks []string
	// TODO(secure): a separate map for quick lookup may be worthwhile for big channels.
	for _, session := range Sessions {
		if !session.Channels[channel] {
			continue
		}
		nicks = append(nicks, session.Nick)
	}

	var replies []*irc.Message

	replies = append(replies, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.JOIN,
		Trailing: channel,
	})
	//replies = append(replies, irc.Message{
	//	Prefix:  &irc.Prefix{Name: *network},
	//	Command: irc.RPL_NOTOPIC,
	//	Params:  []string{channel},
	//})
	// TODO(secure): why the = param?
	replies = append(replies, &irc.Message{
		Command:  irc.RPL_NAMREPLY,
		Params:   []string{s.Nick, "=", channel},
		Trailing: strings.Join(nicks, " "),
	})
	replies = append(replies, &irc.Message{
		Command:  irc.RPL_ENDOFNAMES,
		Params:   []string{s.Nick, channel},
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
	channel := msg.Params[0]
	delete(s.Channels, channel)
	return []*irc.Message{&irc.Message{
		Prefix:  &s.ircPrefix,
		Command: irc.PART,
		Params:  []string{channel},
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
	channel := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	if s.Channels[channel] {
		if len(msg.Params) > 1 && msg.Params[1] == "b" {
			return []*irc.Message{&irc.Message{
				Command:  irc.RPL_ENDOFBANLIST,
				Params:   []string{s.Nick, channel},
				Trailing: "End of Channel Ban List",
			}}
		} else {
			return []*irc.Message{&irc.Message{
				Command: irc.RPL_CHANNELMODEIS,
				Params:  []string{s.Nick, channel, "+"},
			}}
		}
	} else {
		if channel == s.Nick {
			return []*irc.Message{&irc.Message{
				Prefix:   &s.ircPrefix,
				Command:  irc.MODE,
				Params:   []string{s.Nick},
				Trailing: "+",
			}}
		} else {
			return []*irc.Message{&irc.Message{
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{s.Nick, channel},
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
