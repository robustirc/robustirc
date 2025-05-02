package ircserver

import (
	"sort"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["WHO"] = &ircCommand{
		Func: (*IRCServer).cmdWho,
	}
}

func (i *IRCServer) cmdWho(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_ENDOFWHO,
			Params:  []string{s.Nick, "End of /WHO list"},
		})
		return
	}

	// TODO: support WHO on nicknames
	channelname := msg.Params[0]

	lastmsg := &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_ENDOFWHO,
		Params:  []string{s.Nick, channelname, "End of /WHO list"},
	}

	lcChan := ChanToLower(channelname)
	c, ok := i.channels[lcChan]
	if !ok {
		i.sendUser(s, reply, lastmsg)
		return
	}

	if c.modes['s'] {
		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			i.sendUser(s, reply, lastmsg)
			return
		}
	}

	nicks := make([]string, 0, len(c.nicks))
	for nick := range c.nicks {
		if i.nicks[nick].modes['i'] && !s.Channels[lcChan] {
			continue
		}
		nicks = append(nicks, i.nicks[nick].Nick)
	}

	sort.Strings(nicks)

	for _, nick := range nicks {
		session := i.nicks[NickToLower(nick)]
		prefix := session.ircPrefix
		// TODO: also list all other usermodes
		goneStatus := "H"
		if session.AwayMsg != "" {
			goneStatus = "G"
		}
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_WHOREPLY,
			Params:  []string{s.Nick, channelname, prefix.User, prefix.Host, i.ServerPrefix.Name, prefix.Name, goneStatus, "0 " + session.Realname},
		})
	}

	i.sendUser(s, reply, lastmsg)
}
