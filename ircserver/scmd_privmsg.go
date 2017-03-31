package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_PRIVMSG"] = &ircCommand{
		Func: (*IRCServer).cmdServerPrivmsg,
	}
	Commands["server_NOTICE"] = &ircCommand{
		Func: (*IRCServer).cmdServerPrivmsg,
	}
}

// The only difference is that we re-use (and augment) the msg.Prefix instead of setting s.Prefix.
// TODO(secure): refactor this with cmdPrivmsg possibly?
func (i *IRCServer) cmdServerPrivmsg(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NORECIPIENT,
			Params:  []string{msg.Prefix.Name, fmt.Sprintf("No recipient given (%s)", msg.Command)},
		})
		return
	}

	if msg.Trailing() == "" {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTEXTTOSEND,
			Params:  []string{msg.Prefix.Name, "No text to send"},
		})
		return
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		c, ok := i.channels[ChanToLower(msg.Params[0])]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{msg.Prefix.Name, msg.Params[0], "No such channel"},
			})
			return
		}
		i.sendChannel(c, reply, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: msg.Command,
			Params:  []string{msg.Params[0], msg.Trailing()},
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{msg.Prefix.Name, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	i.sendUser(session, reply, &irc.Message{
		Prefix:  servicesPrefix(msg.Prefix),
		Command: msg.Command,
		Params:  []string{msg.Params[0], msg.Trailing()},
	})
}
