package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_INVITE"] = &ircCommand{
		Func:      (*IRCServer).cmdServerInvite,
		MinParams: 2,
	}
}

// TODO(secure): refactor this with cmdInvite possibly?
func (i *IRCServer) cmdServerInvite(s *Session, reply *Replyctx, msg *irc.Message) {
	nickname := msg.Params[0]
	channelname := msg.Params[1]

	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{msg.Prefix.Name, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{msg.Prefix.Name, msg.Params[1], "No such channel"},
		})
		return
	}

	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_USERONCHANNEL,
			Params:  []string{msg.Prefix.Name, session.Nick, c.name, "is already on channel"},
		})
		return
	}

	session.invitedTo[ChanToLower(channelname)] = true
	i.sendServices(reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_INVITING,
		Params:  []string{msg.Prefix.Name, msg.Params[0], c.name},
	})
	i.sendUser(session, reply, &irc.Message{
		Prefix:  servicesPrefix(msg.Prefix),
		Command: irc.INVITE,
		Params:  []string{session.Nick, c.name},
	})
	i.sendChannel(c, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.NOTICE,
		Params:  []string{c.name, fmt.Sprintf("%s invited %s into the channel.", msg.Prefix.Name, msg.Params[0])},
	})
}
