package ircserver

import (
	"strconv"
	"time"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["TOPIC"] = &ircCommand{
		Func:      (*IRCServer).cmdTopic,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdTopic(s *Session, reply *Replyctx, msg *irc.Message) {
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{s.Nick, channel, "No such channel"},
		})
		return
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing() == "" && len(msg.Params) == 2 {
		if c.modes['t'] && !c.nicks[NickToLower(s.Nick)][chanop] {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_CHANOPRIVSNEEDED,
				Params:  []string{s.Nick, channel, "You're not channel operator"},
			})
			return
		}

		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""

		i.sendChannel(c, reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.TOPIC,
			Params:  []string{channel, msg.Trailing()},
		})
		i.sendServices(reply, &irc.Message{
			Prefix:  &irc.Prefix{Name: s.Nick},
			Command: irc.TOPIC,
			Params:  []string{channel, s.Nick, "0", msg.Trailing()},
		})
		return
	}

	if !s.Channels[ChanToLower(channel)] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOTONCHANNEL,
			Params:  []string{s.Nick, channel, "You're not on that channel"},
		})
		return
	}

	// “TOPIC”, i.e. get the topic.
	if len(msg.Params) == 1 {
		if c.topicTime.IsZero() {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.RPL_NOTOPIC,
				Params:  []string{s.Nick, channel, "No topic is set"},
			})
			return
		}

		// TODO(secure): if the channel is secret, return ERR_NOTONCHANNEL

		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_TOPIC,
			Params:  []string{s.Nick, channel, c.topic},
		})
		i.sendUser(s, reply, &irc.Message{
			Prefix: i.ServerPrefix,
			// RPL_TOPICWHOTIME (ircu-specific, not in the RFC)
			Command: "333",
			Params:  []string{s.Nick, channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10)},
		})
		return
	}

	if c.modes['t'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_CHANOPRIVSNEEDED,
			Params:  []string{s.Nick, channel, "You're not channel operator"},
		})
		return
	}

	c.topicNick = s.Nick
	c.topicTime = s.LastActivity
	c.topic = msg.Trailing()

	i.sendChannel(c, reply, &irc.Message{
		Prefix:  &s.ircPrefix,
		Command: irc.TOPIC,
		Params:  []string{channel, msg.Trailing()},
	})
	i.sendServices(reply, &irc.Message{
		Prefix:  &irc.Prefix{Name: s.Nick},
		Command: irc.TOPIC,
		Params:  []string{channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10), msg.Trailing()},
	})
}
