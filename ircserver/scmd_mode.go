package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["server_MODE"] = &ircCommand{
		Func: (*IRCServer).cmdServerMode,
	}
}

func (i *IRCServer) cmdServerMode(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHCHANNEL,
			Params:  []string{msg.Prefix.Name, msg.Params[0], "No such nick/channel"},
		})
		return
	}

	// TODO(secure): possibly refactor this with cmdMode()
	modes := normalizeModes(msg)
	for _, mode := range modes {
		char := mode.Mode[1]
		newvalue := (mode.Mode[0] == '+')

		switch char {
		case 't', 's', 'r', 'i':
			c.modes[char] = newvalue
		case 'o':
			nick := mode.Param
			perms, ok := c.nicks[NickToLower(nick)]
			if !ok {
				i.sendServices(reply, &irc.Message{
					Prefix:  i.ServerPrefix,
					Command: irc.ERR_USERNOTINCHANNEL,
					Params:  []string{msg.Prefix.Name, nick, channelname, "They aren't on that channel"},
				})
			} else {
				// If the user already is a chanop, silently do
				// nothing (like UnrealIRCd).
				if perms[chanop] != newvalue {
					c.nicks[NickToLower(nick)][chanop] = newvalue
				}
			}
		default:
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_UNKNOWNMODE,
				Params:  []string{msg.Prefix.Name, string(char), "is unknown mode char to me"},
			})
		}
	}
	if reply.replyid > 0 {
		return
	}
	i.sendChannel(c, reply, &irc.Message{
		Prefix:  servicesPrefix(msg.Prefix),
		Command: irc.MODE,
		Params:  append([]string{channelname}, modeCmds(modes).IRCParams()...),
	})
}
