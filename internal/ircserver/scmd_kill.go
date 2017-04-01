package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_KILL"] = &ircCommand{
		Func:      (*IRCServer).cmdServerKill,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdServerKill(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 2 {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NEEDMOREPARAMS,
			Params:  []string{"*", msg.Command, "Not enough parameters"},
		})
		return
	}

	killPrefix := msg.Prefix
	for id, session := range i.sessions {
		if id.Id != s.Id.Id || id.Reply == 0 || NickToLower(session.Nick) != NickToLower(msg.Prefix.Name) {
			continue
		}
		killPrefix = &session.ircPrefix
		break
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

	killPath := fmt.Sprintf("ircd!%s!%s", killPrefix.Host, killPrefix.Name)
	killPath = strings.Replace(killPath, "!!", "!", -1)

	i.sendUser(session, reply, &irc.Message{
		Prefix:  killPrefix,
		Command: irc.KILL,
		Params:  []string{session.Nick, fmt.Sprintf("%s (%s)", killPath, msg.Trailing())},
	})
	i.sendServices(reply,
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  &session.ircPrefix,
			Command: irc.QUIT,
			Params:  []string{"Killed: " + msg.Trailing()},
		}))
	i.DeleteSession(session, reply.msgid)
}
