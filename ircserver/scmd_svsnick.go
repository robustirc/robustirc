package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_SVSNICK"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsnick,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdServerSvsnick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “SVSNICK blArgh Guest30503 :1425036445”
	if !IsValidNickname(msg.Params[1]) {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_ERRONEUSNICKNAME,
			Params:  []string{"*", msg.Params[1], "Erroneous nickname"},
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{"*", msg.Params[0], "No such nick/channel"},
		})
		return
	}

	// TODO(secure): kill this code duplication with cmdNick()
	oldPrefix := session.ircPrefix
	oldNick := NickToLower(msg.Params[0])
	session.Nick = msg.Params[1]
	i.nicks[NickToLower(session.Nick)] = session
	delete(i.nicks, oldNick)
	for _, c := range i.channels {
		if modes, ok := c.nicks[oldNick]; ok {
			c.nicks[NickToLower(session.Nick)] = modes
		}
		delete(c.nicks, oldNick)
	}
	session.updateIrcPrefix()
	i.sendServices(reply,
		i.sendCommonChannels(session, reply,
			i.sendUser(session, reply, &irc.Message{
				Prefix:  &oldPrefix,
				Command: irc.NICK,
				Params:  []string{session.Nick},
			})))
}
