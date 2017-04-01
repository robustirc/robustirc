package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["QUIT"] = &ircCommand{
		Func: (*IRCServer).cmdQuit,
	}
}

func (i *IRCServer) cmdQuit(s *Session, reply *Replyctx, msg *irc.Message) {
	i.DeleteSession(s, reply.msgid)
	if s.loggedIn {
		i.sendServices(reply,
			i.sendCommonChannels(s, reply, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: irc.QUIT,
				Params:  []string{msg.Trailing()},
			}))
		i.sendUser(s, reply, &irc.Message{
			Command: irc.ERROR,
			Params:  []string{fmt.Sprintf("Closing Link: %s[%s] (%s)", s.Nick, s.ircPrefix.Host, msg.Trailing())},
		})
	}

}
