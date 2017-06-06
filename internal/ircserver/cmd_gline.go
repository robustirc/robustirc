package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["GLINE"] = &ircCommand{
		Func:      (*IRCServer).cmdGline,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdGline(s *Session, reply *Replyctx, msg *irc.Message) {
	if !s.Operator {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOPRIVILEGES,
			Params:  []string{s.Nick, "Permission Denied - You're not an IRC operator"},
		})
		return

	}

	nick := NickToLower(msg.Params[0])
	session, ok := i.nicks[nick]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{s.Nick, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	if session.RemoteAddr == "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.NOTICE,
			Params:  []string{s.Nick, fmt.Sprintf("Cannot kill %s: no IP address known yet", session.Nick)},
		})
		return
	}

	i.ConfigMu.Lock()
	defer i.ConfigMu.Unlock()
	i.Config.Banned[session.RemoteAddr] = msg.Trailing()

	i.cmdKill(s, reply, msg)
}
