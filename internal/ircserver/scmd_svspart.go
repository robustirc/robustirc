package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_SVSPART"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvspart,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdServerSvspart(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSPART <nick> <chan>
	nick := NickToLower(msg.Params[0])
	channelname := msg.Params[1]

	session, ok := i.nicks[nick]
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
			Params:  []string{msg.Prefix.Name, channelname, "No such channel"},
		})
		return
	}

	if _, ok := c.nicks[nick]; !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTONCHANNEL,
			Params:  []string{msg.Prefix.Name, channelname, "You're not on that channel"},
		})
		return
	}

	i.sendServices(reply, i.sendChannel(c, reply, &irc.Message{
		Prefix:  &session.ircPrefix,
		Command: irc.PART,
		Params:  []string{channelname},
	}))

	delete(c.nicks, nick)
	i.maybeDeleteChannelLocked(c)
	delete(session.Channels, ChanToLower(channelname))
}
