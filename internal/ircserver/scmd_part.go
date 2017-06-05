package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_PART"] = &ircCommand{
		Func: (*IRCServer).cmdServerPart,
	}
}

func (i *IRCServer) cmdServerPart(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ PART #noname-ev” (after enforcing AKICK).
	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{msg.Prefix.Name, channelname, "No such channel"},
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(msg.Prefix.Name)]; !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOTONCHANNEL,
				Params:  []string{msg.Prefix.Name, channelname, "You're not on that channel"},
			})
			continue
		}
		session, _ := i.nicks[NickToLower(msg.Prefix.Name)]

		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: irc.PART,
			Params:  []string{channelname},
		})

		// TODO(secure): reduce code duplication with cmdPart()
		delete(c.nicks, NickToLower(msg.Prefix.Name))
		i.maybeDeleteChannelLocked(c)
		delete(session.Channels, ChanToLower(channelname))
	}
}
