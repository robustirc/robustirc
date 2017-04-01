package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["PING"] = &ircCommand{
		Func: (*IRCServer).cmdPing,
	}
}

func (i *IRCServer) cmdPing(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOORIGIN,
			Params:  []string{s.Nick, "No origin specified"},
		})
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.PONG,
		Params:  []string{msg.Params[0]},
	})
}
