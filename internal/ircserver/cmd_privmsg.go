package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["PRIVMSG"] = &ircCommand{
		Func: (*IRCServer).cmdPrivmsg,
	}
	Commands["NOTICE"] = &ircCommand{
		Func: (*IRCServer).cmdPrivmsg,
	}
}

func (i *IRCServer) cmdPrivmsg(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NORECIPIENT,
			Params:  []string{s.Nick, fmt.Sprintf("No recipient given (%s)", msg.Command)},
		})
		return
	}

	if len(msg.Params) < 2 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTEXTTOSEND,
			Params:  []string{s.Nick, "No text to send"},
		})
		return
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		c, ok := i.channels[ChanToLower(msg.Params[0])]
		if !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{s.Nick, msg.Params[0], "No such channel"},
			})
			return
		}
		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok && c.modes['n'] {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_CANNOTSENDTOCHAN,
				Params:  []string{s.Nick, c.name, "Cannot send to channel"},
			})
			return
		}
		i.sendChannelButOne(c, s, reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: msg.Command,
			Params:  []string{msg.Params[0], msg.Trailing()},
		})
		return
	} else if strings.HasPrefix(msg.Params[0], "$") {
		if s.Operator {
			i.sendAllUsers(reply, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: msg.Command,
				Params:  []string{msg.Params[0], msg.Trailing()},
			})
			return
		}
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOPRIVILEGES,
			Params:  []string{s.Nick, "Permission Denied - You're not an IRC operator"},
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{s.Nick, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	if session.modes['G'] {
		// To message invisible-callerid users, you must share a channel with them.
		common := false
		for channelname := range session.Channels {
			if _, ok := s.Channels[channelname]; ok {
				common = true
				break
			}
		}
		if !common {
			return
		}
	}

	i.sendUser(session, reply, &irc.Message{
		Prefix:  &s.ircPrefix,
		Command: msg.Command,
		Params:  []string{msg.Params[0], msg.Trailing()},
	})

	if session.AwayMsg != "" && msg.Command == irc.PRIVMSG {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_AWAY,
			Params:  []string{s.Nick, msg.Params[0], session.AwayMsg},
		})
	}
}
