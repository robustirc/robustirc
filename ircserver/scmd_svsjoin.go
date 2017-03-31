package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_SVSJOIN"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsjoin,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdServerSvsjoin(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSJOIN <nick> <chan>
	nick := NickToLower(msg.Params[0])
	channelname := msg.Params[1]

	session, ok := i.nicks[nick]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{msg.Prefix.Name, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	if !IsValidChannel(channelname) {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{msg.Prefix.Name, channelname, "No such channel"},
		})
		return
	}
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		c = &channel{
			name:  channelname,
			nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
		}
		i.channels[ChanToLower(channelname)] = c
	}
	if _, ok := c.nicks[nick]; ok {
		return
	}
	c.nicks[nick] = &[maxChanMemberStatus]bool{}
	// If the channel did not exist before, the first joining user becomes a
	// channel operator.
	if !ok {
		c.nicks[nick][chanop] = true
	}
	session.Channels[ChanToLower(channelname)] = true

	i.sendChannel(c, reply, &irc.Message{
		Prefix:  &session.ircPrefix,
		Command: irc.JOIN,
		Params:  []string{channelname},
	})
	var prefix string
	if c.nicks[nick][chanop] {
		prefix = prefix + string('@')
	}
	i.sendServices(reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: "SJOIN",
		Params:  []string{"1", channelname, prefix + session.Nick},
	})
	// Integrate the topic response by simulating a TOPIC command.
	i.cmdTopic(session, reply, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})
	i.cmdNames(session, reply, &irc.Message{Command: irc.NAMES, Params: []string{channelname}})
}
