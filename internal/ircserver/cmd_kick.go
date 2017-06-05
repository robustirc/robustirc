package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["KICK"] = &ircCommand{
		Func:      (*IRCServer).cmdKick,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdKick(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{s.Nick, channelname, "No such nick/channel"},
		})
		return
	}

	perms, ok := c.nicks[NickToLower(s.Nick)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTONCHANNEL,
			Params:  []string{s.Nick, channelname, "You're not on that channel"},
		})
		return
	}

	if !perms[chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_CHANOPRIVSNEEDED,
			Params:  []string{s.Nick, channelname, "You're not channel operator"},
		})
		return
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_USERNOTINCHANNEL,
			Params:  []string{s.Nick, msg.Params[1], channelname, "They aren't on that channel"},
		})
		return
	}

	// Must exist since c.nicks contains the nick.
	session, _ := i.nicks[NickToLower(msg.Params[1])]

	i.sendServices(reply,
		i.sendChannel(c, reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.KICK,
			Params:  []string{msg.Params[0], msg.Params[1], msg.Trailing()},
		}))

	// TODO(secure): reduce code duplication with cmdPart()
	delete(c.nicks, NickToLower(msg.Params[1]))
	i.maybeDeleteChannelLocked(c)
	delete(session.Channels, ChanToLower(channelname))

}
