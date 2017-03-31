package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["USER"] = &ircCommand{
		Func:      (*IRCServer).cmdUser,
		MinParams: 3,
	}
}

func (i *IRCServer) cmdUser(s *Session, reply *Replyctx, msg *irc.Message) {
	// We keep the username (so that bans are more effective) and realname
	// (some people actually set it and look at it).
	s.Username = msg.Params[0]
	s.Realname = msg.Trailing()
	s.updateIrcPrefix()
	i.maybeLogin(s, reply, msg)
}
