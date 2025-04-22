package ircserver

import (
	"sort"
	"strconv"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["LIST"] = &ircCommand{
		Func: (*IRCServer).cmdList,
	}
}

func (i *IRCServer) cmdList(s *Session, reply *Replyctx, msg *irc.Message) {
	channels := make([]string, 0, len(i.channels))
	var filter []string
	if len(msg.Params) > 0 {
		if stripped := strings.TrimSpace(msg.Params[0]); stripped != "" {
			filter = strings.Split(stripped, ",")
		}
	}
	if len(filter) > 0 {
		for _, channel := range filter {
			channelname := ChanToLower(strings.TrimSpace(channel))
			if _, ok := i.channels[channelname]; ok {
				channels = append(channels, string(channelname))
			}
		}
	} else {
		for channel := range i.channels {
			channels = append(channels, string(channel))
		}
		sort.Strings(channels)
	}
	for _, channel := range channels {
		c := i.channels[lcChan(channel)]
		if c.modes['s'] && !s.Operator && !s.Channels[lcChan(channel)] {
			continue
		}
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_LIST,
			Params:  []string{s.Nick, c.name, strconv.Itoa(len(c.nicks)), c.topic},
		})
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_LISTEND,
		Params:  []string{s.Nick, "End of LIST"},
	})
}
