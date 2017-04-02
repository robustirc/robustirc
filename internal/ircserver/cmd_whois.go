package ircserver

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["WHOIS"] = &ircCommand{
		Func:      (*IRCServer).cmdWhois,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdWhois(s *Session, reply *Replyctx, msg *irc.Message) {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{s.Nick, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_WHOISUSER,
		Params:  []string{s.Nick, session.Nick, session.ircPrefix.User, session.ircPrefix.Host, "*", session.Realname},
	})

	var channels []string
	for channel := range session.Channels {
		var prefix string
		c := i.channels[channel]
		if c.modes['s'] && !s.Operator && !s.Channels[channel] {
			continue
		}
		if c.nicks[NickToLower(session.Nick)][chanop] {
			prefix = "@"
		}
		channels = append(channels, prefix+c.name)
	}

	sort.Strings(channels)

	if len(channels) > 0 {
		// TODO(secure): this needs to be split into multiple messages if the line exceeds 510 bytes.
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_WHOISCHANNELS,
			Params:  []string{s.Nick, session.Nick, strings.Join(channels, " ")},
		})
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_WHOISSERVER,
		Params:  []string{s.Nick, session.Nick, i.ServerPrefix.Name, "RobustIRC"},
	})

	if session.Operator {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_WHOISOPERATOR,
			Params:  []string{s.Nick, session.Nick, "is an IRC operator"},
		})
	}

	if session.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.RPL_AWAY,
			Params:  []string{s.Nick, session.Nick, session.AwayMsg},
		})
	}

	idle := strconv.FormatInt(int64(s.LastActivity.Sub(session.LastNonPing).Seconds()), 10)
	signon := strconv.FormatInt(time.Unix(0, session.Created).Unix(), 10)
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_WHOISIDLE,
		Params:  []string{s.Nick, session.Nick, idle, signon, "seconds idle, signon time"},
	})

	if session.modes['r'] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: "307", // RPL_WHOISREGNICK (not in the RFC)
			Params:  []string{s.Nick, session.Nick, "user has identified to services"},
		})
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_ENDOFWHOIS,
		Params:  []string{s.Nick, session.Nick, "End of /WHOIS list"},
	})
}
