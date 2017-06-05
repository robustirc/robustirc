package ircserver

import (
	"fmt"
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["JOIN"] = &ircCommand{
		Func:      (*IRCServer).cmdJoin,
		MinParams: 1,
	}
}

func banned(bans []banPattern, userhost, userhostAddr string) bool {
	for _, b := range bans {
		if b.re.MatchString(userhost) || b.re.MatchString(userhostAddr) {
			return true
		}
	}
	return false
}

func (i *IRCServer) cmdJoin(s *Session, reply *Replyctx, msg *irc.Message) {
	var keys []string
	if len(msg.Params) > 1 {
		keys = strings.Split(msg.Params[1], ",")
	}
	for idx, channelname := range strings.Split(msg.Params[0], ",") {
		var key string
		if idx <= len(keys)-1 {
			key = keys[idx]
		}
		if !IsValidChannel(channelname) {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_NOSUCHCHANNEL,
				Params:  []string{s.Nick, channelname, "No such channel"},
			})
			continue
		}
		var modesmsg *irc.Message
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			if got, limit := uint64(len(i.channels)), i.ChannelLimit(); got >= limit && limit > 0 {
				i.sendUser(s, reply, &irc.Message{
					Prefix:  i.ServerPrefix,
					Command: irc.ERR_NOSUCHCHANNEL,
					Params:  []string{s.Nick, channelname, "No such channel"},
				})
				continue
			}

			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			c.modes['n'] = true
			c.modes['t'] = true
			modesmsg = &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: "MODE",
				Params:  []string{channelname, "+nt"},
			}
			i.channels[ChanToLower(channelname)] = c
		} else if c.modes['i'] && !s.invitedTo[ChanToLower(channelname)] {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_INVITEONLYCHAN,
				Params:  []string{s.Nick, c.name, "Cannot join channel (+i)"},
			})
			continue
		} else if c.modes['x'] && !s.invitedTo[ChanToLower(channelname)] {
			if err := i.verifyCaptcha(s, key); err != nil {
				captchaUrl := i.generateCaptchaURL(s, fmt.Sprintf("join:%d:%s", s.LastActivity.UnixNano(), c.name))
				i.sendUser(s, reply, &irc.Message{
					Prefix:  i.ServerPrefix,
					Command: irc.NOTICE,
					Params:  []string{s.Nick, "To join " + c.name + ", please go to " + captchaUrl},
				})
				i.sendUser(s, reply, &irc.Message{
					Prefix:  i.ServerPrefix,
					Command: irc.ERR_INVITEONLYCHAN,
					Params:  []string{s.Nick, c.name, "Cannot join channel (+x). Please go to " + captchaUrl},
				})
				captchaChallengesSent.Inc()
				continue
			}
		} else if banned(c.bans, s.ircPrefix.String(), s.Nick+"!"+s.Username+"@"+s.remoteAddr) {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_BANNEDFROMCHAN,
				Params:  []string{s.Nick, c.name, "Cannot join channel (+b)"},
			})
			continue
		}
		// Invites are only valid once.
		if c.modes['i'] || c.modes['x'] {
			delete(s.invitedTo, ChanToLower(channelname))
		}
		if _, ok := c.nicks[NickToLower(s.Nick)]; ok {
			continue
		}
		c.nicks[NickToLower(s.Nick)] = &[maxChanMemberStatus]bool{}
		// If the channel did not exist before, the first joining user becomes a
		// channel operator.
		if !ok {
			c.nicks[NickToLower(s.Nick)][chanop] = true
		}
		s.Channels[ChanToLower(channelname)] = true

		i.sendChannel(c, reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.JOIN,
			Params:  []string{channelname},
		})
		if modesmsg != nil {
			i.sendChannel(c, reply, modesmsg)
		}
		var prefix string
		if c.nicks[NickToLower(s.Nick)][chanop] {
			prefix = prefix + string('@')
		}
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: "SJOIN",
			Params:  []string{"1", channelname, prefix + s.Nick},
		})
		// Channel joins integrate the output of MODE, TOPIC and NAMES commands:
		i.cmdMode(s, reply, &irc.Message{Command: irc.MODE, Params: []string{channelname}})
		i.cmdTopic(s, reply, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})
		i.cmdNames(s, reply, &irc.Message{Command: irc.NAMES, Params: []string{channelname}})
	}
}
