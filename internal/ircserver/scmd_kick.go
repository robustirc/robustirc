package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_KICK"] = &ircCommand{
		Func:      (*IRCServer).cmdServerKick,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdServerKick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ KICK #noname-ev blArgh_ :get out”
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{msg.Prefix.Name, channelname, "No such nick/channel"},
		})
		return
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_USERNOTINCHANNEL,
			Params:  []string{msg.Prefix.Name, msg.Params[1], channelname, "They aren't on that channel"},
		})
		return
	}

	// Must exist since c.nicks contains the nick.
	session, _ := i.nicks[NickToLower(msg.Params[1])]

	i.sendServices(reply, i.sendChannel(c, reply, &irc.Message{
		Prefix: &irc.Prefix{
			Name: msg.Prefix.Name,
			User: "services",
			Host: "services",
		},
		Command: irc.KICK,
		Params:  []string{msg.Params[0], msg.Params[1], msg.Trailing()},
	}))

	// TODO(secure): reduce code duplication with cmdPart()
	delete(c.nicks, NickToLower(msg.Params[1]))
	i.maybeDeleteChannel(c)
	delete(session.Channels, ChanToLower(channelname))
}
