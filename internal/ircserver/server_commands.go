package ircserver

import (
	"sort"

	"gopkg.in/sorcix/irc.v2"
)

func init() {
	// When developing the anope RobustIRC module, I used the following command
	// to make sure all commands which anope sends are implemented:
	//
	// perl -nlE 'my ($cmd) = ($_ =~ /send_cmd\([^,]+, "([^" ]+)/); say $cmd if defined($cmd)' src/protocol/robustirc.c | sort | uniq

	// NB: This command doesn’t have the server_ prefix because it is sent in
	// order to make a session _become_ a server. Having the function in this
	// file makes more sense than in commands.go.
	Commands["SERVER"] = &ircCommand{Func: (*IRCServer).cmdServer, MinParams: 2}
}

func servicesPrefix(prefix *irc.Prefix) *irc.Prefix {
	return &irc.Prefix{
		Name: prefix.Name,
		User: "services",
		Host: "services"}
}

func (i *IRCServer) cmdServer(s *Session, reply *Replyctx, msg *irc.Message) {
	authenticated := false
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	for _, service := range i.Config.IRC.Services {
		if s.Pass == "services="+service.Password {
			authenticated = true
			break
		}
	}
	if !authenticated {
		i.sendUser(s, reply, &irc.Message{
			Command: irc.ERROR,
			Params:  []string{"Invalid password"},
		})
		return
	}
	s.Server = true
	s.ircPrefix = irc.Prefix{
		Name: msg.Params[0],
	}
	i.serverSessions = append(i.serverSessions, s.Id.Id)
	i.sendServices(reply, &irc.Message{
		Command: "SERVER",
		Params: []string{
			i.ServerPrefix.Name,
			"1",  // hopcount
			"23", // token, must be different from the services token
		},
	})
	nicks := make([]string, 0, len(i.nicks))
	for nick := range i.nicks {
		nicks = append(nicks, string(nick))
	}
	sort.Strings(nicks)
	for _, nick := range nicks {
		session := i.nicks[lcNick(nick)]
		// Skip sessions that are not yet logged in, sessions that represent a
		// server connection and subsessions of a server connection.
		if !session.loggedIn || session.Server || session.Id.Reply != 0 {
			continue
		}
		modestr := "+"
		for mode := 'A'; mode < 'z'; mode++ {
			if session.modes[mode] {
				modestr += string(mode)
			}
		}
		i.sendServices(reply, &irc.Message{
			Command: irc.NICK,
			Params: []string{
				session.Nick,
				"1", // hopcount (ignored by anope)
				"1", // timestamp
				session.Username,
				session.ircPrefix.Host,
				i.ServerPrefix.Name,
				session.svid,
				modestr,
				session.Realname,
			},
		})
		channelnames := make([]string, 0, len(session.Channels))
		for channelname := range session.Channels {
			channelnames = append(channelnames, string(channelname))
		}
		sort.Strings(channelnames)
		for _, channelname := range channelnames {
			var prefix string

			if i.channels[lcChan(channelname)].nicks[NickToLower(session.Nick)][chanop] {
				prefix = prefix + string('@')
			}
			i.sendServices(reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: "SJOIN",
				Params:  []string{"1", i.channels[lcChan(channelname)].name, prefix + session.Nick},
			})
		}
	}
}
