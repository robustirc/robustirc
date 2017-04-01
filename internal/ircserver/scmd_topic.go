package ircserver

import (
	"fmt"
	"strconv"
	"time"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_TOPIC"] = &ircCommand{
		Func:      (*IRCServer).cmdServerTopic,
		MinParams: 3,
	}
}

func (i *IRCServer) cmdServerTopic(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ TOPIC #chaos-hd ChanServ 0 :”
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{msg.Prefix.Name, channel, "No such channel"},
		})
		return
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing() == "" && len(msg.Params) == 2 {
		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""
		i.sendChannel(c, reply, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: irc.TOPIC,
			Params:  []string{channel, msg.Trailing()},
		})
		return
	}

	ts, err := strconv.ParseInt(msg.Params[2], 0, 64)
	if err != nil {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NEEDMOREPARAMS,
			Params:  []string{"*", channel, fmt.Sprintf("Could not parse timestamp: %v", err)},
		})
		return
	}

	c.topicNick = msg.Params[1]
	c.topicTime = time.Unix(ts, 0)
	c.topic = msg.Trailing()

	i.sendChannel(c, reply, &irc.Message{
		Prefix:  servicesPrefix(msg.Prefix),
		Command: irc.TOPIC,
		Params:  []string{channel, msg.Trailing()},
	})
}
