package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["server_SVSMODE"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsmode,
		MinParams: 2,
	}
}

func (i *IRCServer) cmdServerSvsmode(s *Session, reply *Replyctx, msg *irc.Message) {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_NOSUCHNICK,
			Params:  []string{"*", msg.Params[0], "No such nick/channel"},
		})
		return
	}
	modestr := msg.Params[1]
	if !strings.HasPrefix(modestr, "+") && !strings.HasPrefix(modestr, "-") {
		i.sendServices(reply, &irc.Message{
			Prefix:  i.ServerPrefix,
			Command: irc.ERR_UMODEUNKNOWNFLAG,
			Params:  []string{"*", "Unknown MODE flag"},
		})
		return
	}
	modes := normalizeModes(msg)

	// true for adding a mode, false for removing it
	for _, mode := range modes {
		newvalue := (mode.Mode[0] == '+')
		char := mode.Mode[1]
		switch char {
		case 'd':
			session.svid = mode.Param
		case 'r':
			// Store registered flag
			session.modes[char] = newvalue
		default:
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.ERR_UMODEUNKNOWNFLAG,
				Params:  []string{"*", "Unknown MODE flag"},
			})
		}
	}
	modestr = "+"
	for mode := 'A'; mode < 'z'; mode++ {
		if session.modes[mode] {
			modestr += string(mode)
		}
	}
	i.sendUser(session, reply, &irc.Message{
		Prefix:  &s.ircPrefix,
		Command: irc.MODE,
		Params:  []string{session.Nick, modestr},
	})
}
