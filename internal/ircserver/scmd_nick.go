package ircserver

import (
	"hash/fnv"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_NICK"] = &ircCommand{
		Func: (*IRCServer).cmdServerNick,
	}
}

func (i *IRCServer) cmdServerNick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “NICK OperServ 1 1422134861 services localhost.net services.localhost.net 0 :Operator Server”
	// <nickname> <hopcount> <username> <host> <servertoken> <umode> <realname>

	// Could be either a nickchange or the introduction of a new user.
	if len(msg.Params) == 1 {
		// TODO(secure): handle nickchanges. not sure when/if those are used. botserv maybe?
		return
	}

	if _, ok := i.nicks[NickToLower(msg.Params[0])]; ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NICKNAMEINUSE,
			Params:  []string{"*", msg.Params[0], "Nickname is already in use"},
		})
		return
	}

	h := fnv.New64()
	h.Write([]byte(msg.Params[0]))
	id := robust.Id{
		Id:    s.Id.Id,
		Reply: int64(h.Sum64()),
	}

	i.CreateSession(id, "")
	ss := i.sessions[id]
	ss.Nick = msg.Params[0]
	i.nicks[NickToLower(ss.Nick)] = ss
	ss.Username = msg.Params[3]
	ss.Realname = msg.Trailing()
	ss.updateIrcPrefix()
}
