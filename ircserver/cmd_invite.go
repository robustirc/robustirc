package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["INVITE"] = &ircCommand{
		Func:      (*IRCServer).cmdInvite,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdInvite(s *Session, reply *Replyctx, msg *irc.Message) {
	nickname := msg.Params[0]
	channelname := msg.Params[1]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTONCHANNEL,
			Params:  []string{s.Nick, msg.Params[1], "You're not on that channel"},
		})
		return
	}
	if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTONCHANNEL,
			Params:  []string{s.Nick, msg.Params[1], "You're not on that channel"},
		})
		return
	}
	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{s.Nick, msg.Params[0], "No such nick/channel"},
		})
		return
	}
	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_USERONCHANNEL,
			Params:  []string{s.Nick, session.Nick, c.name, "is already on channel"},
		})
		return
	}
	if c.modes['i'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_CHANOPRIVSNEEDED,
			Params:  []string{s.Nick, c.name, "You're not channel operator"},
		})
		return
	}
	session.invitedTo[ChanToLower(channelname)] = true
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_INVITING,
		Params:  []string{s.Nick, session.Nick, c.name},
	})
	i.sendServices(reply,
		i.sendUser(session, reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.INVITE,
			Params:  []string{session.Nick, c.name},
		}))
	i.sendChannel(c, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.NOTICE,
		Params:  []string{c.name, fmt.Sprintf("%s invited %s into the channel.", s.Nick, msg.Params[0])},
	})

	if session.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_AWAY,
			Params:  []string{s.Nick, msg.Params[0], session.AwayMsg},
		})
	}
}
