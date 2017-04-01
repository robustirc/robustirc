package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["AWAY"] = &ircCommand{
		Func: (*IRCServer).cmdAway,
	}
}

func (i *IRCServer) cmdAway(s *Session, reply *Replyctx, msg *irc.Message) {
	s.AwayMsg = strings.TrimSpace(msg.Trailing())
	if s.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_NOWAWAY,
			Params:  []string{s.Nick, "You have been marked as being away"},
		})
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_UNAWAY,
		Params:  []string{s.Nick, "You are no longer marked as being away"},
	})
}
