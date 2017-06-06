package ircserver

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/robustirc/robustirc/internal/robust"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["MODE"] = &ircCommand{
		Func:      (*IRCServer).cmdMode,
		MinParams: 1,
	}
}

// resolveSessionToRemoteAddrLocked replaces session references such as
// “@robust/0x1234” in patterns with the corresponding remote address,
// e.g. “@127.0.0.1”.
func (i *IRCServer) resolveSessionToRemoteAddrLocked(pattern string) string {
	idx := strings.Index(pattern, "@robust/0x")
	if idx == -1 {
		return pattern
	}
	id, err := strconv.ParseInt(pattern[idx+len("@robust/"):], 0, 64)
	if err != nil {
		return pattern
	}
	s, ok := i.sessions[robust.Id{Id: uint64(id)}]
	if !ok || s.RemoteAddr == "" {
		return pattern
	}
	return pattern[:idx] + "@" + s.RemoteAddr
}

func ban(c *channel, add bool, banmask, pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	if add {
		c.bans = append(c.bans, banPattern{re: re, pattern: banmask})
		return nil
	}
	// remove ban
	newBans := make([]banPattern, 0, len(c.bans))
	for _, b := range c.bans {
		if b.pattern == banmask {
			continue
		}
		newBans = append(newBans, b)
	}
	c.bans = newBans
	return nil
}

func banBoth(c *channel, add bool, banmask, pattern, patternAddr string) error {
	if err := ban(c, add, banmask, pattern); err != nil {
		return err
	}
	if patternAddr != pattern {
		return ban(c, add, banmask, patternAddr)
	}
	return nil
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
			if mode.Mode != "+b" || mode.Param != "" {
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

				case 'b':
					// The only supported repetition operator is “*”, which will
					// be turned into “.*”.
					pattern := regexp.QuoteMeta(mode.Param)
					pattern = strings.Replace(pattern, "\\*", ".*", -1)
					patternAddr := i.resolveSessionToRemoteAddrLocked(pattern)

					if err := banBoth(c, newvalue, mode.Param, pattern, patternAddr); err != nil {
						i.sendUser(s, reply, &irc.Message{
							Prefix:  i.ServerPrefix,
							Command: irc.ERR_UNKNOWNMODE,
							Params:  []string{s.Nick, "+b", fmt.Sprintf("%q is not a valid regexp: %v", mode.Param, err)},
						})
					} else {
						queryOnly = false
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
					seen := make(map[string]bool)
					for _, b := range c.bans {
						seen[b.pattern] = true
					}
					patterns := make([]string, 0, len(seen))
					for pattern := range seen {
						patterns = append(patterns, pattern)
					}
					sort.Strings(patterns)
					for _, pattern := range patterns {
						i.sendUser(s, reply, &irc.Message{
							Prefix:  i.ServerPrefix,
							Command: irc.RPL_BANLIST,
							Params:  []string{s.Nick, channelname, pattern},
						})
					}
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
