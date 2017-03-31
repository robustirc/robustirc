package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_QUIT"] = &ircCommand{
		Func: (*IRCServer).cmdServerQuit,
	}
}

func (i *IRCServer) cmdServerQuit(s *Session, reply *Replyctx, msg *irc.Message) {
	// No prefix means the server quits the entire session.
	if msg.Prefix == nil {
		i.DeleteSession(s, reply.msgid)
		// For services, we also need to delete all sessions that share the
		// same .Id, but have a different .Reply.
		for id, session := range i.sessions {
			if id.Id != s.Id.Id || id.Reply == 0 {
				continue
			}
			i.sendCommonChannels(session, reply, &irc.Message{
				Prefix:  &session.ircPrefix,
				Command: irc.QUIT,
				Params:  []string{msg.Trailing()},
			})
			i.DeleteSession(session, reply.msgid)
		}
		return
	}

	// We got a prefix, so only a single session quits (e.g. nickname
	// enforcer).
	for id, session := range i.sessions {
		if id.Id != s.Id.Id || id.Reply == 0 || NickToLower(session.Nick) != NickToLower(msg.Prefix.Name) {
			continue
		}
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  &session.ircPrefix,
			Command: irc.QUIT,
			Params:  []string{msg.Trailing()},
		})
		i.DeleteSession(session, reply.msgid)
		return
	}
}
