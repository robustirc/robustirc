package ircserver

import "gopkg.in/sorcix/irc.v2"

func init() {
	Commands["MODE"] = &ircCommand{
		Func:      (*IRCServer).cmdMode,
		MinParams: 1,
	}
}

func (i *IRCServer) cmdMode(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	if s.Channels[ChanToLower(channelname)] {
		// Channel must exist, the user is in it.
		c := i.channels[ChanToLower(channelname)]
		modes := normalizeModes(msg)
		queryOnly := true

		if len(modes) == 0 {
			modestr := "+"
			for mode := 'A'; mode < 'z'; mode++ {
				if c.modes[mode] {
					modestr += string(mode)
				}
			}
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.RPL_CHANNELMODEIS,
				Params:  []string{s.Nick, channelname, modestr},
			})
			return
		}

		isChanOp := c.nicks[NickToLower(s.Nick)][chanop] || s.Operator

		for _, mode := range modes {
			char := mode.Mode[1]
			if mode.Mode != "+b" {
				// Non-query modes
				queryOnly = false
				if !isChanOp {
					i.sendUser(s, reply, &irc.Message{
						Prefix:  i.ServerPrefix,
						Command: irc.ERR_CHANOPRIVSNEEDED,
						Params:  []string{s.Nick, channelname, "You're not channel operator"},
					})
					return
				}
				newvalue := (mode.Mode[0] == '+')
				switch char {
				case 't', 's', 'i', 'n':
					c.modes[char] = newvalue

				case 'x':
					if i.captchaConfigured() {
						c.modes[char] = newvalue
					} else {
						i.sendUser(s, reply, &irc.Message{
							Prefix:  i.ServerPrefix,
							Command: irc.NOTICE,
							Params:  []string{s.Nick, "Cannot set mode +x, no CaptchaURL/CaptchaHMACSecret configured"},
						})
					}

				case 'o':
					nick := mode.Param
					perms, ok := c.nicks[NickToLower(nick)]
					if !ok {
						i.sendUser(s, reply, &irc.Message{
							Prefix:  i.ServerPrefix,
							Command: irc.ERR_USERNOTINCHANNEL,
							Params:  []string{s.Nick, nick, channelname, "They aren't on that channel"},
						})
					} else {
						// If the user already is a chanop, silently do
						// nothing (like UnrealIRCd).
						if perms[chanop] != newvalue {
							c.nicks[NickToLower(nick)][chanop] = newvalue
						}
					}
				default:
					i.sendUser(s, reply, &irc.Message{
						Prefix:  i.ServerPrefix,
						Command: irc.ERR_UNKNOWNMODE,
						Params:  []string{s.Nick, string(char), "is unknown mode char to me"},
					})
				}
			} else {
				// Query modes
				switch char {
				case 'b':
					i.sendUser(s, reply, &irc.Message{
						Prefix:  i.ServerPrefix,
						Command: irc.RPL_ENDOFBANLIST,
						Params:  []string{s.Nick, channelname, "End of Channel Ban List"},
					})

				default:
					i.sendUser(s, reply, &irc.Message{
						Prefix:  i.ServerPrefix,
						Command: irc.ERR_UNKNOWNMODE,
						Params:  []string{s.Nick, string(char), "is unknown mode char to me"},
					})
				}
			}
		}

		if queryOnly {
			return
		}

		if reply.replyid > 0 {
			// TODO(secure): see how other ircds are handling mixtures of valid/invalid modes. do they sanity check the entire mode string before applying it, or do they keep valid modes while erroring for others?
			return
		}
		i.sendServices(reply,
			i.sendChannel(c, reply, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: irc.MODE,
				Params:  append([]string{channelname}, modeCmds(modes).IRCParams()...),
			}))
		return
	}

	nick := NickToLower(channelname)
	if session, ok := i.nicks[nick]; ok {
		if nick != NickToLower(s.Nick) &&
			!s.Operator {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_USERSDONTMATCH,
				Params:  []string{s.Nick, "Can't change mode for other users"},
			})
			return
		}
		modes := normalizeModes(msg)

		if len(modes) == 0 {
			modestr := "+"
			for mode := 'A'; mode < 'z'; mode++ {
				if session.modes[mode] {
					modestr += string(mode)
				}
			}
			i.sendServices(reply,
				i.sendUser(s, reply, &irc.Message{
					Prefix:  &s.ircPrefix,
					Command: irc.MODE,
					Params:  []string{session.Nick, modestr},
				}))

		} else {
			for _, mode := range modes {
				char := mode.Mode[1]
				newvalue := (mode.Mode[0] == '+')
				switch char {
				case 'i':
					session.modes[char] = newvalue
				}
			}

			// It would be nice to send the confirmation to s as well
			// (in case s != session), but at least irssi gets
			// confused and applies the mode change to the current
			// user, not the destination user.
			i.sendServices(reply,
				i.sendUser(session, reply, &irc.Message{
					Prefix:  &s.ircPrefix,
					Command: irc.MODE,
					Params:  []string{session.Nick, modeCmds(modes).IRCParams()[0]},
				}))
		}
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.ERR_NOTONCHANNEL,
		Params:  []string{s.Nick, channelname, "You're not on that channel"},
	})
	return
}
