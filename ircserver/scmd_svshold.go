package ircserver

import (
	"fmt"
	"time"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_SVSHOLD"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvshold,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdServerSvshold(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSHOLD <nick> [<expirationtimerelative> :<reason>]
	nick := NickToLower(msg.Params[0])
	if len(msg.Params) > 1 {
		duration, err := time.ParseDuration(msg.Params[1] + "s")
		if err != nil {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.NOTICE,
				Params:  []string{s.ircPrefix.Name, fmt.Sprintf("Invalid duration: %v", err)},
			})
			return
		}
		i.svsholds[nick] = svshold{
			added:    s.LastActivity,
			duration: duration,
			reason:   msg.Trailing(),
		}
	} else {
		delete(i.svsholds, nick)
	}
}
