package ircserver

import (
	"strings"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	Commands["PASS"] = &ircCommand{
		Func: (*IRCServer).cmdPass,
	}
}

func (i *IRCServer) cmdPass(s *Session, reply *Replyctx, msg *irc.Message) {
	// TODO(secure): document this in the admin/user manual
	// You can specify multiple passwords in a single PASS command, separated
	// by colons and prefixed with <key>=, e.g. “nickserv=secret” or
	// “nickserv=secret:network=letmein” in case the network requires a
	// password _and_ you want to authenticate to nickserv.
	//
	// In case there is no <key>= prefix, nickserv= is added.
	//
	// The valid prefixes are:
	// services= for identifying as a server-to-server connection (services)
	// session= for picking up a saved session (not yet implemented)
	// network= for authenticating to a private network (not yet implemented)
	// nickserv= for authenticating to services
	// oper= for authenticating as an IRC operator
	if len(msg.Params) > 0 {
		s.Pass = strings.Join(msg.Params, " ")
	}
	if !strings.HasPrefix(s.Pass, "nickserv=") &&
		!strings.HasPrefix(s.Pass, "services=") &&
		!strings.HasPrefix(s.Pass, "network=") &&
		!strings.HasPrefix(s.Pass, "oper=") &&
		!strings.HasPrefix(s.Pass, "session=") &&
		!strings.HasPrefix(s.Pass, "captcha=") {
		s.Pass = "nickserv=" + s.Pass
	}

	i.maybeLogin(s, reply, msg)
}
