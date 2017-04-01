package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["PART"] = &ircCommand{
		Func:      (*IRCServer).cmdPart,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdPart(s *Session, reply *Replyctx, msg *irc.Message) {
	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{s.Nick, channelname, "No such channel"},
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOTONCHANNEL,
				Params:  []string{s.Nick, channelname, "You're not on that channel"},
			})
			continue
		}

		i.sendServices(reply,
			i.sendChannel(c, reply, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: irc.PART,
				Params:  []string{channelname},
			}))

		delete(c.nicks, NickToLower(s.Nick))
		i.maybeDeleteChannel(c)
		delete(s.Channels, ChanToLower(channelname))

	}
}
