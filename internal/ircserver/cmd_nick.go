package ircserver

import (
	"fmt"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["NICK"] = &ircCommand{
		Func: (*IRCServer).cmdNick,
	}
}

func (i *IRCServer) cmdNick(s *Session, reply *Replyctx, msg *irc.Message) {
	oldPrefix := s.ircPrefix

	var nick string
	if len(msg.Params) > 0 {
		nick = msg.Params[0]
	}
	if nick == "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NONICKNAMEGIVEN,
			Params:  []string{"No nickname given"},
		})
		return
	}

	dest := "*"
	onlyCapsChanged := false // Whether the nick change only changes capitalization.
	if s.loggedIn {
		dest = s.Nick
		onlyCapsChanged = NickToLower(nick) == NickToLower(dest)
	}

	if !IsValidNickname(nick) {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_ERRONEUSNICKNAME,
			Params:  []string{dest, nick, "Erroneous nickname"},
		})
		return
	}

	if _, ok := i.nicks[NickToLower(nick)]; (ok && !onlyCapsChanged) || IsServicesNickname(nick) {
		i.sendUser(s, reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NICKNAMEINUSE,
			Params:  []string{dest, nick, "Nickname is already in use"},
		})
		return
	}

	if hold, ok := i.svsholds[NickToLower(nick)]; ok {
		if !s.LastActivity.After(hold.added.Add(hold.duration)) {
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_ERRONEUSNICKNAME,
				Params:  []string{dest, nick, fmt.Sprintf("Erroneous Nickname: %s", hold.reason)},
			})
			return
		}
		// The SVSHOLD expired, so remove it.
		delete(i.svsholds, NickToLower(nick))
	}

	if s.Nick == nick {
		// No change: the user already *has* that nickname.
		return
	}

	oldNick := NickToLower(s.Nick)
	s.Nick = nick
	i.nicks[NickToLower(s.Nick)] = s
	if oldNick != "" && !onlyCapsChanged {
		delete(i.nicks, oldNick)
		for _, c := range i.channels {
			// Check ok to ensure we never assign the default value (<nil>).
			if modes, ok := c.nicks[oldNick]; ok {
				c.nicks[NickToLower(s.Nick)] = modes
			}
			delete(c.nicks, oldNick)
		}
	}
	s.updateIrcPrefix()

	if oldNick != "" {
		i.sendServices(reply,
			i.sendCommonChannels(s, reply,
				i.sendUser(s, reply, &irc.Message{
					Prefix:  &oldPrefix,
					Command: irc.NICK,
					Params:  []string{nick},
				})))
		return
	}

	i.maybeLogin(s, reply, msg)
}
