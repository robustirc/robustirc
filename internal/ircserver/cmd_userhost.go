package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["USERHOST"] = &ircCommand{
		Func:      (*IRCServer).cmdUserhost,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdUserhost(s *Session, reply *Replyctx, msg *irc.Message) {
	var userhosts []string
	for _, nickname := range msg.Params {
		session, ok := i.nicks[NickToLower(nickname)]
		if !ok {
			continue
		}
		awayPrefix := "+"
		if session.AwayMsg != "" {
			awayPrefix = "-"
		}
		nick := session.Nick
		if session.Operator {
			nick = nick + "*"
		}
		userhosts = append(userhosts, fmt.Sprintf("%s=%s%s", nick, awayPrefix, session.ircPrefix.String()))
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_USERHOST,
		Params:  []string{s.Nick, strings.Join(userhosts, " ")},
	})
}
