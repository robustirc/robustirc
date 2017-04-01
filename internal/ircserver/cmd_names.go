package ircserver

import (
	"sort"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["NAMES"] = &ircCommand{
		Func: (*IRCServer).cmdNames,
	}
}

func (i *IRCServer) cmdNames(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) > 0 {
		channelname := msg.Params[0]
		if c, ok := i.channels[ChanToLower(channelname)]; ok {
			nicks := make([]string, 0, len(c.nicks))
			for nick, perms := range c.nicks {
				var prefix string
				if perms[chanop] {
					prefix = prefix + string('@')
				}
				nicks = append(nicks, prefix+i.nicks[nick].Nick)
			}

			sort.Strings(nicks)

			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.RPL_NAMREPLY,
				Params:  []string{s.Nick, "=", channelname, strings.Join(nicks, " ")},
			})

			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.RPL_ENDOFNAMES,
				Params:  []string{s.Nick, channelname, "End of /NAMES list."},
			})
			return
		}
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_ENDOFNAMES,
		Params:  []string{s.Nick, "*", "End of /NAMES list."},
	})
}
