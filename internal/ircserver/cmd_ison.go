package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["ISON"] = &ircCommand{
		Func:      (*IRCServer).cmdIson,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdIson(s *Session, reply *Replyctx, msg *irc.Message) {
	var onlineUsers []string
	for _, nickname := range msg.Params {
		if session, ok := i.nicks[NickToLower(nickname)]; ok {
			onlineUsers = append(onlineUsers, session.Nick)
		}
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_ISON,
		Params:  []string{s.Nick, strings.Join(onlineUsers, " ")},
	})
}
