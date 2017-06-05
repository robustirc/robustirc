package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["KILL"] = &ircCommand{
		Func:      (*IRCServer).cmdKill,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdKill(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 2 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NEEDMOREPARAMS,
			Params:  []string{s.Nick, msg.Command, "Not enough parameters"},
		})
		return
	}

	if !s.Operator {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOPRIVILEGES,
			Params:  []string{s.Nick, "Permission Denied - You're not an IRC operator"},
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{s.Nick, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	i.deleteSessionLocked(session, reply.msgid)

	i.sendServices(reply,
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  &session.ircPrefix,
			Command: irc.QUIT,
			Params:  []string{"Killed by " + s.Nick + ": " + msg.Trailing()},
		}))

	i.sendUser(session, reply, &irc.Message{
		Prefix:  &s.ircPrefix,
		Command: irc.KILL,
		Params:  []string{session.Nick, fmt.Sprintf("ircd!%s!%s (%s)", s.ircPrefix.Host, s.Nick, msg.Trailing())},
	})

	i.sendUser(session, reply, &irc.Message{
		Command: irc.ERROR,
		Params:  []string{fmt.Sprintf("Closing Link: %s[%s] (Killed (%s (%s)))", session.Nick, session.ircPrefix.Host, s.Nick, msg.Trailing())},
	})
}
