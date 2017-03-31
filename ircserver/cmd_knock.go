package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["KNOCK"] = &ircCommand{
		Func:      (*IRCServer).cmdKnock,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdKnock(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: "480",
			Params:  []string{s.Nick, fmt.Sprintf("Cannot knock on %s (Channel does not exist)", channelname)},
		})
		return
	}

	if !c.modes['i'] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: "480",
			Params:  []string{s.Nick, fmt.Sprintf("Cannot knock on %s (Channel is not invite only)", channelname)},
		})
		return
	}

	reason := "no reason specified"
	if len(msg.Params) > 1 {
		reason = strings.Join(msg.Params[1:], " ")
	}

	i.sendChannel(c, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.NOTICE,
		Params:  []string{c.name, fmt.Sprintf("[Knock] by %s (%s)", s.ircPrefix.String(), reason)},
	})
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.NOTICE,
		Params:  []string{s.Nick, fmt.Sprintf("Knocked on %s", c.name)},
	})
}
