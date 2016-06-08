package ircserver

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
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

	// These just use exactly the same code as clients. We can directly assign
	// the contents of Commands[x] because commands.go is sorted lexically
	// before server_commands.go. For details, see
	// http://golang.org/ref/spec#Package_initialization.
	Commands["server_PING"] = Commands["PING"]

	Commands["server_QUIT"] = &ircCommand{Func: (*IRCServer).cmdServerQuit}
	Commands["server_NICK"] = &ircCommand{
		Func: (*IRCServer).cmdServerNick,
		// Compaction is handled by Commands["NICK"]
	}
	Commands["server_MODE"] = &ircCommand{
		Func: (*IRCServer).cmdServerMode,
		// Compaction is handled by Commands["MODE"]
	}
	Commands["server_JOIN"] = &ircCommand{
		Func:                   (*IRCServer).cmdServerJoin,
		CompactionCreate:       createServerJoin,
		CompactionPrepareStmt:  prepareStmtServerJoin,
		CompactionPrepareViews: prepareViewsServerJoin,
		CompactionDropViews:    dropViewsServerJoin,
		Compact:                compactServerJoin,
	}
	Commands["server_PART"] = &ircCommand{
		CompactionCreate:       createServerPart,
		CompactionPrepareStmt:  prepareStmtServerPart,
		CompactionPrepareViews: prepareViewsServerPart,
		CompactionDropViews:    dropViewsServerPart,
		Compact:                compactServerPart,
		Func:                   (*IRCServer).cmdServerPart,
	}
	Commands["server_PRIVMSG"] = &ircCommand{
		Func: (*IRCServer).cmdServerPrivmsg,
		ImmediatelyCompactable: true,
	}
	Commands["server_NOTICE"] = &ircCommand{
		Func: (*IRCServer).cmdServerPrivmsg,
		ImmediatelyCompactable: true,
	}
	Commands["server_TOPIC"] = &ircCommand{
		Func:      (*IRCServer).cmdServerTopic,
		MinParams: 3,
		// Compaction is handled by Commands["TOPIC"]
	}
	Commands["server_SVSNICK"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsnick,
		MinParams: 2,
		// Compaction is handled by Commands["NICK"]
	}
	Commands["server_SVSMODE"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsmode,
		MinParams: 2,
		// Compaction is handled by Commands["MODE"]
	}
	Commands["server_SVSHOLD"] = &ircCommand{Func: (*IRCServer).cmdServerSvshold, MinParams: 1}
	Commands["server_SVSJOIN"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvsjoin,
		MinParams: 2,
		// Compaction is handled by Commands["JOIN"]
	}
	Commands["server_SVSPART"] = &ircCommand{
		Func:      (*IRCServer).cmdServerSvspart,
		MinParams: 2,
		// Compaction is handled by Commands["PART"]
	}
	Commands["server_KILL"] = &ircCommand{
		Func:      (*IRCServer).cmdServerKill,
		MinParams: 1,
		// Compaction is handled by Commands["KILL"]
	}
	Commands["server_KICK"] = &ircCommand{
		Func:      (*IRCServer).cmdServerKick,
		MinParams: 2,
		// Compaction is handled by Commands["KICK"]
	}
	Commands["server_INVITE"] = &ircCommand{
		Func:      (*IRCServer).cmdServerInvite,
		MinParams: 2,
		// Compaction is handled by Commands["INVITE"]
	}
}

func servicesPrefix(prefix *irc.Prefix) *irc.Prefix {
	return &irc.Prefix{
		Name: prefix.Name,
		User: "services",
		Host: "services"}
}

func (i *IRCServer) cmdServerKick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ KICK #noname-ev blArgh_ :get out”
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, channelname},
			Trailing: "No such nick/channel",
		})
		return
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_USERNOTINCHANNEL,
			Params:   []string{msg.Prefix.Name, msg.Params[1], channelname},
			Trailing: "They aren't on that channel",
		})
		return
	}

	// Must exist since c.nicks contains the nick.
	session, _ := i.nicks[NickToLower(msg.Params[1])]

	i.sendServices(reply, i.sendChannel(c, reply, &irc.Message{
		Prefix: &irc.Prefix{
			Name: msg.Prefix.Name,
			User: "services",
			Host: "services",
		},
		Command:       irc.KICK,
		Params:        []string{msg.Params[0], msg.Params[1]},
		Trailing:      msg.Trailing,
		EmptyTrailing: true,
	}))

	// TODO(secure): reduce code duplication with cmdPart()
	delete(c.nicks, NickToLower(msg.Params[1]))
	i.maybeDeleteChannel(c)
	delete(session.Channels, ChanToLower(channelname))
	i.CompactionDatabase.ExecStmt("KICK", reply.msgid, s.Id.Id, session.Id.Id, channelname)
}

func (i *IRCServer) cmdServerKill(s *Session, reply *Replyctx, msg *irc.Message) {
	if strings.TrimSpace(msg.Trailing) == "" {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{"*", msg.Command},
			Trailing: "Not enough parameters",
		})
		return
	}

	killPrefix := msg.Prefix
	for id, session := range i.sessions {
		if id.Id != s.Id.Id || id.Reply == 0 || NickToLower(session.Nick) != NickToLower(msg.Prefix.Name) {
			continue
		}
		killPrefix = &session.ircPrefix
		break
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	killPath := fmt.Sprintf("ircd!%s!%s", killPrefix.Host, killPrefix.Name)
	killPath = strings.Replace(killPath, "!!", "!", -1)

	i.sendUser(session, reply, &irc.Message{
		Prefix:   killPrefix,
		Command:  irc.KILL,
		Params:   []string{session.Nick},
		Trailing: fmt.Sprintf("%s (%s)", killPath, msg.Trailing),
	})
	i.sendServices(reply,
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:   &session.ircPrefix,
			Command:  irc.QUIT,
			Trailing: "Killed: " + msg.Trailing,
		}))
	i.DeleteSession(session)
	i.CompactionDatabase.ExecStmt("KILL", reply.msgid, s.Id.Id, session.Id.Id)
}

func (i *IRCServer) cmdServerQuit(s *Session, reply *Replyctx, msg *irc.Message) {
	// No prefix means the server quits the entire session.
	if msg.Prefix == nil {
		i.DeleteSession(s)
		// For services, we also need to delete all sessions that share the
		// same .Id, but have a different .Reply.
		for id, session := range i.sessions {
			if id.Id != s.Id.Id || id.Reply == 0 {
				continue
			}
			i.sendCommonChannels(session, reply, &irc.Message{
				Prefix:        &session.ircPrefix,
				Command:       irc.QUIT,
				Trailing:      msg.Trailing,
				EmptyTrailing: true,
			})
			i.DeleteSession(session)
		}
		return
	}

	// We got a prefix, so only a single session quits (e.g. nickname
	// enforcer).
	for id, session := range i.sessions {
		if id.Id != s.Id.Id || id.Reply == 0 || NickToLower(session.Nick) != NickToLower(msg.Prefix.Name) {
			continue
		}
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:        &session.ircPrefix,
			Command:       irc.QUIT,
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		i.DeleteSession(session)
		return
	}
}

func (i *IRCServer) cmdServerNick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “NICK OperServ 1 1422134861 services localhost.net services.localhost.net 0 :Operator Server”
	// <nickname> <hopcount> <username> <host> <servertoken> <umode> <realname>

	// Could be either a nickchange or the introduction of a new user.
	if len(msg.Params) == 1 {
		// TODO(secure): handle nickchanges. not sure when/if those are used. botserv maybe?
		return
	}

	if _, ok := i.nicks[NickToLower(msg.Params[0])]; ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NICKNAMEINUSE,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "Nickname is already in use",
		})
		return
	}

	h := fnv.New64()
	h.Write([]byte(msg.Params[0]))
	id := types.RobustId{
		Id:    s.Id.Id,
		Reply: int64(h.Sum64()),
	}

	i.CreateSession(id, "")
	ss, _ := i.GetSession(id)
	ss.Nick = msg.Params[0]
	i.nicks[NickToLower(ss.Nick)] = ss
	ss.Username = msg.Params[3]
	ss.Realname = msg.Trailing
	ss.updateIrcPrefix()
}

func (i *IRCServer) cmdServerMode(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
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
			i.CompactionDatabase.ExecStmt("MODE", reply.msgid, s.Id.Id, sql.NullInt64{Valid: false}, channelname, mode.Mode,
				sql.NullString{
					String: mode.Param,
					Valid:  mode.Param != ""})
		case 'o':
			nick := mode.Param
			perms, ok := c.nicks[NickToLower(nick)]
			if !ok {
				i.sendServices(reply, &irc.Message{
					Prefix:   i.ServerPrefix,
					Command:  irc.ERR_USERNOTINCHANNEL,
					Params:   []string{msg.Prefix.Name, nick, channelname},
					Trailing: "They aren't on that channel",
				})
			} else {
				// If the user already is a chanop, silently do
				// nothing (like UnrealIRCd).
				if perms[chanop] != newvalue {
					c.nicks[NickToLower(nick)][chanop] = newvalue
					i.CompactionDatabase.ExecStmt("MODE", reply.msgid, s.Id.Id, sql.NullInt64{Valid: false}, channelname, mode.Mode,
						sql.NullString{
							String: mode.Param,
							Valid:  mode.Param != ""})
				}
			}
		default:
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_UNKNOWNMODE,
				Params:   []string{msg.Prefix.Name, string(char)},
				Trailing: "is unknown mode char to me",
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

func (i *IRCServer) cmdServer(s *Session, reply *Replyctx, msg *irc.Message) {
	authenticated := false
	for _, service := range i.Config.Services {
		if s.Pass == "services="+service.Password {
			authenticated = true
			break
		}
	}
	if !authenticated {
		i.sendUser(s, reply, &irc.Message{
			Command:  irc.ERROR,
			Trailing: "Invalid password",
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
		if !session.loggedIn() || session.Server || session.Id.Reply != 0 {
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
			},
			Trailing:      session.Realname,
			EmptyTrailing: true,
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
				Prefix:   i.ServerPrefix,
				Command:  "SJOIN",
				Params:   []string{"1", i.channels[lcChan(channelname)].name},
				Trailing: prefix + session.Nick,
			})
		}
	}
}

func (i *IRCServer) cmdServerSvshold(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSHOLD <nick> [<expirationtimerelative> :<reason>]
	nick := NickToLower(msg.Params[0])
	if len(msg.Params) > 1 {
		duration, err := time.ParseDuration(msg.Params[1] + "s")
		if err != nil {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.NOTICE,
				Params:   []string{s.ircPrefix.Name},
				Trailing: fmt.Sprintf("Invalid duration: %v", err),
			})
			return
		}
		i.svsholds[nick] = svshold{
			added:    s.LastActivity,
			duration: duration,
			reason:   msg.Trailing,
		}
	} else {
		delete(i.svsholds, nick)
	}
}

func (i *IRCServer) cmdServerSvsjoin(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSJOIN <nick> <chan>
	nick := NickToLower(msg.Params[0])
	channelname := msg.Params[1]

	session, ok := i.nicks[nick]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	if !IsValidChannel(channelname) {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, channelname},
			Trailing: "No such channel",
		})
		return
	}
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		c = &channel{
			name:  channelname,
			nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
		}
		i.channels[ChanToLower(channelname)] = c
	}
	if _, ok := c.nicks[nick]; ok {
		return
	}
	c.nicks[nick] = &[maxChanMemberStatus]bool{}
	// If the channel did not exist before, the first joining user becomes a
	// channel operator.
	if !ok {
		c.nicks[nick][chanop] = true
	}
	session.Channels[ChanToLower(channelname)] = true

	i.sendChannel(c, reply, &irc.Message{
		Prefix:   &session.ircPrefix,
		Command:  irc.JOIN,
		Trailing: channelname,
	})
	var prefix string
	if c.nicks[nick][chanop] {
		prefix = prefix + string('@')
	}
	i.sendServices(reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  "SJOIN",
		Params:   []string{"1", channelname},
		Trailing: prefix + session.Nick,
	})
	// Integrate the topic response by simulating a TOPIC command.
	i.cmdTopic(session, reply, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})
	i.cmdNames(session, reply, &irc.Message{Command: irc.NAMES, Params: []string{channelname}})

	i.CompactionDatabase.ExecStmt("JOIN", reply.msgid, s.Id.Id, session.Id.Id, channelname)
	i.CompactionDatabase.ExecStmt("_all_target", session.Id.Id, reply.msgid)
}

func (i *IRCServer) cmdServerSvspart(s *Session, reply *Replyctx, msg *irc.Message) {
	// SVSPART <nick> <chan>
	nick := NickToLower(msg.Params[0])
	channelname := msg.Params[1]

	session, ok := i.nicks[nick]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, channelname},
			Trailing: "No such channel",
		})
		return
	}

	if _, ok := c.nicks[nick]; !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{msg.Prefix.Name, channelname},
			Trailing: "You're not on that channel",
		})
		return
	}

	i.sendServices(reply, i.sendChannel(c, reply, &irc.Message{
		Prefix:  &session.ircPrefix,
		Command: irc.PART,
		Params:  []string{channelname},
	}))

	delete(c.nicks, nick)
	i.maybeDeleteChannel(c)
	delete(session.Channels, ChanToLower(channelname))

	i.CompactionDatabase.ExecStmt("PART", reply.msgid, s.Id.Id, session.Id.Id, channelname)
}

func (i *IRCServer) cmdServerSvsmode(s *Session, reply *Replyctx, msg *irc.Message) {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}
	modestr := msg.Params[1]
	if !strings.HasPrefix(modestr, "+") && !strings.HasPrefix(modestr, "-") {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_UMODEUNKNOWNFLAG,
			Params:   []string{"*"},
			Trailing: "Unknown MODE flag",
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
			i.CompactionDatabase.ExecStmt("MODE", reply.msgid, s.Id.Id, session.Id.Id, session.Nick, mode.Mode,
				sql.NullString{
					String: mode.Param,
					Valid:  mode.Param != ""})
			i.CompactionDatabase.ExecStmt("_all_target", session.Id.Id, reply.msgid)
		case 'r':
			// Store registered flag
			session.modes[char] = newvalue
			i.CompactionDatabase.ExecStmt("MODE", reply.msgid, s.Id.Id, session.Id.Id, session.Nick, mode.Mode,
				sql.NullString{
					String: mode.Param,
					Valid:  mode.Param != ""})
			i.CompactionDatabase.ExecStmt("_all_target", session.Id.Id, reply.msgid)
		default:
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_UMODEUNKNOWNFLAG,
				Params:   []string{"*"},
				Trailing: "Unknown MODE flag",
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
		Prefix:   &s.ircPrefix,
		Command:  irc.MODE,
		Params:   []string{session.Nick},
		Trailing: modestr,
	})
}

func (i *IRCServer) cmdServerSvsnick(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “SVSNICK blArgh Guest30503 :1425036445”
	if !IsValidNickname(msg.Params[1]) {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_ERRONEUSNICKNAME,
			Params:   []string{"*", msg.Params[1]},
			Trailing: "Erroneous nickname",
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{"*", msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	// TODO(secure): kill this code duplication with cmdNick()
	oldPrefix := session.ircPrefix
	oldNick := NickToLower(msg.Params[0])
	session.Nick = msg.Params[1]
	i.nicks[NickToLower(session.Nick)] = session
	delete(i.nicks, oldNick)
	for _, c := range i.channels {
		if modes, ok := c.nicks[oldNick]; ok {
			c.nicks[NickToLower(session.Nick)] = modes
		}
		delete(c.nicks, oldNick)
	}
	session.updateIrcPrefix()
	i.sendServices(reply,
		i.sendCommonChannels(session, reply,
			i.sendUser(session, reply, &irc.Message{
				Prefix:   &oldPrefix,
				Command:  irc.NICK,
				Trailing: session.Nick,
			})))
	i.CompactionDatabase.ExecStmt("NICK", reply.msgid, s.Id.Id, session.Id.Id, session.Nick)
	i.CompactionDatabase.ExecStmt("_all_target", session.Id.Id, reply.msgid)
}

func createServerJoin(db *sql.DB) error {
	// We cannot make msgid unique because one JOIN message may contain
	// multiple channels.
	const createStmt = `
		CREATE TABLE paramsServerJoin (msgid integer not null, session integer not null, subsession integer not null, channel text not null collate nocase);
		CREATE INDEX paramsServerJoinMsgid ON paramsServerJoin (msgid);
		CREATE INDEX paramsServerJoinSession ON paramsServerJoin (session, subsession);
		`
	_, err := db.Exec(createStmt)
	return err
}

func prepareStmtServerJoin(p Preparer) (*sql.Stmt, error) {
	return p.Prepare("INSERT INTO paramsServerJoin (msgid, session, subsession, channel) VALUES (?, ?, ?, ?)")
}

func prepareViewsServerJoin(tx *sql.Tx, compactionEnd time.Time) error {
	_, err := tx.Exec(
		fmt.Sprintf("CREATE VIEW paramsServerJoinWin AS SELECT * FROM paramsServerJoin WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func dropViewsServerJoin(tx *sql.Tx) error {
	_, err := tx.Exec("DROP VIEW paramsServerJoinWin")
	return err
}

func compactServerJoin(tx *sql.Tx) error {
	const query = `
CREATE TABLE candidates AS
SELECT
    j.msgid AS join_msgid,
    j.session AS session,
	j.subsession AS subsession,
    j.channel AS channel,
    p.msgid AS part_msgid
FROM
    paramsServerJoinWin AS j
    LEFT JOIN paramsServerPartWin AS p
    ON (
        j.session = p.session AND
		j.subsession = p.subsession AND
        j.msgid < p.msgid AND
        j.channel = p.channel
    )
WHERE
    p.msgid NOT NULL;

-- Delete all (sequences of) JOIN messages which are directly followed by a deleteSession message.
INSERT INTO candidates
SELECT
   j.msgid AS join_msgid,
   j.session AS session,
   j.subsession AS subsession,
   j.channel AS channel,
   d.msgid AS part_msgid
FROM
   (
		SELECT
			js.msgid AS msgid,
			js.session AS session,
			js.subsession AS subsession,
			js.channel AS channel,
			MIN(a.msgid) AS next_msgid
		FROM
			paramsServerJoinWin AS js
			INNER JOIN allMessagesWin AS a
			ON (
				js.session = a.session AND
				(a.irccommand IS NULL OR
				 (a.irccommand != 'JOIN' AND
				  a.irccommand != 'PART')) AND
				a.msgid > js.msgid
			)
		GROUP BY js.msgid, js.channel
	) AS j
	INNER JOIN deleteSessionWin AS d
	ON (
		j.session = d.session AND
		j.next_msgid = d.msgid
	);

-- Retain JOIN messages for multiple channels of which not all have been left.
DELETE FROM
    candidates
WHERE
    join_msgid IN (
        SELECT
            msgid
        FROM
            paramsServerJoinWin AS j
            LEFT JOIN candidates AS c
            ON (
                j.msgid = c.join_msgid AND
                j.channel = c.channel
            )
        WHERE
            c.join_msgid is null
    );

-- sqlite3 cannot drop columns in ALTER TABLE statements, so we need to copy.
CREATE TABLE deleteIds AS SELECT join_msgid AS msgid FROM candidates;
DROP TABLE candidates;

DELETE FROM paramsServerJoin WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := tx.Exec(query)
	return err
}

func (i *IRCServer) cmdServerJoin(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ JOIN #noname-ev” (before enforcing AKICK).

	for _, channelname := range strings.Split(msg.Params[0], ",") {
		if !IsValidChannel(channelname) {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "No such channel",
			})
			continue
		}

		nick := NickToLower(msg.Prefix.Name)
		session, ok := i.nicks[nick]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHNICK,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "No such nick/channel",
			})
			continue
		}

		// TODO(secure): reduce code duplication with cmdJoin()
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			i.channels[ChanToLower(channelname)] = c
		}
		c.nicks[nick] = &[maxChanMemberStatus]bool{}
		// If the channel did not exist before, the first joining user becomes a
		// channel operator.
		if !ok {
			c.nicks[nick][chanop] = true
		}
		session.Channels[ChanToLower(channelname)] = true

		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:   servicesPrefix(msg.Prefix),
			Command:  irc.JOIN,
			Trailing: channelname,
		})

		i.CompactionDatabase.ExecStmt("server_JOIN", reply.msgid, session.Id.Id, session.Id.Reply, channelname)
	}
}

func createServerPart(db *sql.DB) error {
	// We cannot make msgid unique because one JOIN message may contain
	// multiple channels.
	const createStmt = `
		CREATE TABLE paramsServerPart (msgid integer not null, session integer not null, subsession integer not null, channel text not null collate nocase);
		CREATE INDEX paramsServerPartMsgid ON paramsServerPart (msgid);
		CREATE INDEX paramsServerPartSession ON paramsServerPart (session, subsession);
		`
	_, err := db.Exec(createStmt)
	return err
}

func prepareStmtServerPart(p Preparer) (*sql.Stmt, error) {
	return p.Prepare("INSERT INTO paramsServerPart (msgid, session, subsession, channel) VALUES (?, ?, ?, ?)")
}

func prepareViewsServerPart(tx *sql.Tx, compactionEnd time.Time) error {
	_, err := tx.Exec(
		fmt.Sprintf("CREATE VIEW paramsServerPartWin AS SELECT * FROM paramsServerPart WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func dropViewsServerPart(tx *sql.Tx) error {
	_, err := tx.Exec("DROP VIEW paramsServerPartWin")
	return err
}

func compactServerPart(tx *sql.Tx) error {
	const query = `
CREATE TABLE deleteIds AS
SELECT
    p.msgid AS msgid
FROM
    paramsServerPartWin AS p
    LEFT JOIN paramsServerJoinWin AS j
    ON (
        p.session = j.session AND
        p.subsession = j.subsession AND
        p.msgid > j.msgid AND
        p.channel = j.channel
    )
GROUP BY p.msgid
HAVING COUNT(j.msgid) = 0;

DELETE FROM paramsServerPart WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := tx.Exec(query)
	return err
}

func (i *IRCServer) cmdServerPart(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ PART #noname-ev” (after enforcing AKICK).
	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "No such channel",
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(msg.Prefix.Name)]; !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{msg.Prefix.Name, channelname},
				Trailing: "You're not on that channel",
			})
			continue
		}
		session, _ := i.nicks[NickToLower(msg.Prefix.Name)]

		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:  servicesPrefix(msg.Prefix),
			Command: irc.PART,
			Params:  []string{channelname},
		})

		// TODO(secure): reduce code duplication with cmdPart()
		delete(c.nicks, NickToLower(msg.Prefix.Name))
		i.maybeDeleteChannel(c)
		delete(session.Channels, ChanToLower(channelname))

		i.CompactionDatabase.ExecStmt("server_PART", reply.msgid, session.Id.Id, session.Id.Reply, channelname)
	}
}

func (i *IRCServer) cmdServerTopic(s *Session, reply *Replyctx, msg *irc.Message) {
	// e.g. “:ChanServ TOPIC #chaos-hd ChanServ 0 :”
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, channel},
			Trailing: "No such channel",
		})
		return
	}

	// “TOPIC :”, i.e. unset the topic.
	if msg.Trailing == "" && msg.EmptyTrailing {
		c.topicNick = ""
		c.topicTime = time.Time{}
		c.topic = ""
		i.sendChannel(c, reply, &irc.Message{
			Prefix:        servicesPrefix(msg.Prefix),
			Command:       irc.TOPIC,
			Params:        []string{channel},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		i.CompactionDatabase.ExecStmt("TOPIC", reply.msgid, s.Id.Id, channel,
			sql.NullString{
				String: msg.Trailing,
				Valid:  msg.Trailing != "" || msg.EmptyTrailing})
		return
	}

	ts, err := strconv.ParseInt(msg.Params[2], 0, 64)
	if err != nil {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{"*", channel},
			Trailing: fmt.Sprintf("Could not parse timestamp: %v", err),
		})
		return
	}

	c.topicNick = msg.Params[1]
	c.topicTime = time.Unix(ts, 0)
	c.topic = msg.Trailing

	i.sendChannel(c, reply, &irc.Message{
		Prefix:        servicesPrefix(msg.Prefix),
		Command:       irc.TOPIC,
		Params:        []string{channel},
		Trailing:      msg.Trailing,
		EmptyTrailing: true,
	})
	i.CompactionDatabase.ExecStmt("TOPIC", reply.msgid, s.Id.Id, channel,
		sql.NullString{
			String: msg.Trailing,
			Valid:  msg.Trailing != "" || msg.EmptyTrailing})
}

// The only difference is that we re-use (and augment) the msg.Prefix instead of setting s.Prefix.
// TODO(secure): refactor this with cmdPrivmsg possibly?
func (i *IRCServer) cmdServerPrivmsg(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NORECIPIENT,
			Params:   []string{msg.Prefix.Name},
			Trailing: fmt.Sprintf("No recipient given (%s)", msg.Command),
		})
		return
	}

	if msg.Trailing == "" {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTEXTTOSEND,
			Params:   []string{msg.Prefix.Name},
			Trailing: "No text to send",
		})
		return
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		c, ok := i.channels[ChanToLower(msg.Params[0])]
		if !ok {
			i.sendServices(reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{msg.Prefix.Name, msg.Params[0]},
				Trailing: "No such channel",
			})
			return
		}
		i.sendChannel(c, reply, &irc.Message{
			Prefix:        servicesPrefix(msg.Prefix),
			Command:       msg.Command,
			Params:        []string{msg.Params[0]},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	i.sendUser(session, reply, &irc.Message{
		Prefix:        servicesPrefix(msg.Prefix),
		Command:       msg.Command,
		Params:        []string{msg.Params[0]},
		Trailing:      msg.Trailing,
		EmptyTrailing: true,
	})
}

// TODO(secure): refactor this with cmdInvite possibly?
func (i *IRCServer) cmdServerInvite(s *Session, reply *Replyctx, msg *irc.Message) {
	nickname := msg.Params[0]
	channelname := msg.Params[1]

	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{msg.Prefix.Name, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{msg.Prefix.Name, msg.Params[1]},
			Trailing: "No such channel",
		})
		return
	}

	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_USERONCHANNEL,
			Params:   []string{msg.Prefix.Name, session.Nick, c.name},
			Trailing: "is already on channel",
		})
		return
	}

	session.invitedTo[ChanToLower(channelname)] = true
	i.CompactionDatabase.ExecStmt("INVITE", reply.msgid, s.Id.Id, session.Id.Id, channelname)
	i.CompactionDatabase.ExecStmt("_all_target", session.Id.Id, reply.msgid)
	i.sendServices(reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_INVITING,
		Params:  []string{msg.Prefix.Name, msg.Params[0], c.name},
	})
	i.sendUser(session, reply, &irc.Message{
		Prefix:   servicesPrefix(msg.Prefix),
		Command:  irc.INVITE,
		Params:   []string{session.Nick},
		Trailing: c.name,
	})
	i.sendChannel(c, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.NOTICE,
		Params:   []string{c.name},
		Trailing: fmt.Sprintf("%s invited %s into the channel.", msg.Prefix.Name, msg.Params[0]),
	})
}
