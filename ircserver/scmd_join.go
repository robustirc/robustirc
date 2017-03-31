package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_JOIN"] = &ircCommand{
		Func: (*IRCServer).cmdServerJoin,
	}
}

func (i *IRCServer) cmdServerJoin(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ JOIN #noname-ev” (before enforcing AKICK).

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		if !IsValidChannel(channelname) {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{msg.Prefix.Name, channelname, "No such channel"},
			})
			continue
		}

		nick := NickToLower(msg.Prefix.Name)
		session, ok := i.nicks[nick]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHNICK,
				Params:  []string{msg.Prefix.Name, channelname, "No such nick/channel"},
			})
			continue
		}

		// TODO(secure): reduce code duplication with cmdJoin()
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			i.channels[ChanToLower(channelname)] = c
		}
		c.nicks[nick] = &[maxChanMemberStatus]bool{}
		// If the channel did not exist before, the first joining user becomes a
		// channel operator.
		if !ok {
			c.nicks[nick][chanop] = true
		}
		session.Channels[ChanToLower(channelname)] = true

		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: irc.JOIN,
			Params:  []string{channelname},
		})
	}
}
