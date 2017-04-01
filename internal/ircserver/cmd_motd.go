package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["MOTD"] = &ircCommand{
		Func: (*IRCServer).cmdMotd,
	}
}

func (i *IRCServer) cmdMotd(s *Session, reply *Replyctx, msg *irc.Message) {
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_MOTDSTART,
		Params:  []string{s.Nick, "- " + i.ServerPrefix.Name + " Message of the day -"},
	})
	// TODO(secure): make motd configurable
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_MOTD,
		Params:  []string{s.Nick, "- No MOTD configured yet."},
	})
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_ENDOFMOTD,
		Params:  []string{s.Nick, "End of MOTD command"},
	})
}
