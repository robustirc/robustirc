package ircserver

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
)

var (
	Commands = make(map[string]*ircCommand)
)

type ircCommand struct {
	Func func(*IRCServer, *Session, *Replyctx, *irc.Message)

	// ImmediatelyCompactable being true results in these messages being
	// immediately compacted once they are old enough (e.g. for PING).
	ImmediatelyCompactable bool

	// CompactionPrepareStmt creates the necessary tables and returns a
	// prepared statement that will be passed to CompactionInsert for each
	// message of this command.
	CompactionPrepareStmt func(*sql.DB) (*sql.Stmt, error)

	// CompactionPrepareViews is called in each compaction run. The command is
	// expected to create database views that only expose messages that are
	// relevant for a compaction of all messages until |compactionEnd|.
	CompactionPrepareViews func(*sql.DB, time.Time) error

	// CompactionInsert inserts a message using the prepared statement created
	// with CompactionPrepareStmt.
	CompactionInsert func(types.RobustId, types.RobustId, *irc.Message, *sql.Stmt) error

	// Compact is called in each compaction pass and deletes all messages that
	// are no longer relevant. It is expected to store the message ids of all
	// deleted messages in a temporary table called deleteIds.
	Compact func(*sql.DB) error

	// MinParams ensures that enough parameters were specified.
	// irc.ERR_NEEDMOREPARAMS is returned in case less than MinParams
	// parameters were found, otherwise, Func is called.
	MinParams int
}

func init() {
	// Keep this list ordered the same way the functions below are ordered.
	Commands["PING"] = &ircCommand{
		Func: (*IRCServer).cmdPing,
		ImmediatelyCompactable: true,
	}
	Commands["NICK"] = &ircCommand{
		Func: (*IRCServer).cmdNick,
		CompactionPrepareStmt:  prepareStmtNick,
		CompactionPrepareViews: prepareViewsNick,
		CompactionInsert:       insertNick,
		Compact:                compactNick,
	}
	Commands["USER"] = &ircCommand{
		Func:                   (*IRCServer).cmdUser,
		MinParams:              3,
		CompactionPrepareStmt:  prepareStmtUser,
		CompactionPrepareViews: prepareViewsUser,
		CompactionInsert:       insertUser,
		Compact:                compactUser,
	}
	Commands["JOIN"] = &ircCommand{
		Func:                   (*IRCServer).cmdJoin,
		MinParams:              1,
		CompactionPrepareStmt:  prepareStmtJoin,
		CompactionPrepareViews: prepareViewsJoin,
		CompactionInsert:       insertJoin,
		Compact:                compactJoin,
	}
	Commands["PART"] = &ircCommand{
		Func:                   (*IRCServer).cmdPart,
		MinParams:              1,
		CompactionPrepareStmt:  prepareStmtPart,
		CompactionPrepareViews: prepareViewsPart,
		CompactionInsert:       insertPart,
		Compact:                compactPart,
	}
	Commands["KICK"] = &ircCommand{
		Func:      (*IRCServer).cmdKick,
		MinParams: 2,
	}
	Commands["QUIT"] = &ircCommand{
		Func: (*IRCServer).cmdQuit,
		CompactionPrepareStmt:  prepareStmtQuit,
		CompactionPrepareViews: prepareViewsQuit,
		CompactionInsert:       insertQuit,
		Compact:                compactQuit,
	}
	Commands["PRIVMSG"] = &ircCommand{
		Func: (*IRCServer).cmdPrivmsg,
		ImmediatelyCompactable: true,
	}
	Commands["NOTICE"] = &ircCommand{
		Func: (*IRCServer).cmdPrivmsg,
		ImmediatelyCompactable: true,
	}
	Commands["MODE"] = &ircCommand{
		Func:                   (*IRCServer).cmdMode,
		MinParams:              1,
		CompactionPrepareStmt:  prepareStmtMode,
		CompactionPrepareViews: prepareViewsMode,
		CompactionInsert:       insertMode,
		Compact:                compactMode,
		// TODO: relevantMode, start with removing read-only MODE, e.g. MODE #chaos-hd or MODE #chaos-hd b or MODE #chaos-hd +b
	}
	Commands["WHO"] = &ircCommand{
		Func: (*IRCServer).cmdWho,
		ImmediatelyCompactable: true,
	}
	Commands["OPER"] = &ircCommand{Func: (*IRCServer).cmdOper, MinParams: 2}
	Commands["KILL"] = &ircCommand{
		Func:      (*IRCServer).cmdKill,
		MinParams: 1,
	}
	Commands["AWAY"] = &ircCommand{
		Func: (*IRCServer).cmdAway,
		CompactionPrepareStmt:  prepareStmtAway,
		CompactionPrepareViews: prepareViewsAway,
		CompactionInsert:       insertAway,
		Compact:                compactAway,
	}
	Commands["TOPIC"] = &ircCommand{
		Func:                   (*IRCServer).cmdTopic,
		MinParams:              1,
		CompactionPrepareStmt:  prepareStmtTopic,
		CompactionPrepareViews: prepareViewsTopic,
		CompactionInsert:       insertTopic,
		Compact:                compactTopic,
	}
	Commands["MOTD"] = &ircCommand{
		Func: (*IRCServer).cmdMotd,
		ImmediatelyCompactable: true,
	}
	Commands["WHOIS"] = &ircCommand{
		Func:                   (*IRCServer).cmdWhois,
		MinParams:              1,
		ImmediatelyCompactable: true,
	}
	Commands["LIST"] = &ircCommand{
		Func: (*IRCServer).cmdList,
		ImmediatelyCompactable: true,
	}
	Commands["INVITE"] = &ircCommand{
		Func:      (*IRCServer).cmdInvite,
		MinParams: 2,
	}
	Commands["USERHOST"] = &ircCommand{
		Func: (*IRCServer).cmdUserhost,
		ImmediatelyCompactable: true,
		MinParams:              1,
	}
	Commands["NAMES"] = &ircCommand{
		Func: (*IRCServer).cmdNames,
		ImmediatelyCompactable: true,
	}
	Commands["KNOCK"] = &ircCommand{
		Func: (*IRCServer).cmdKnock,
		ImmediatelyCompactable: true,
		MinParams:              1,
	}
	serviceAlias := &ircCommand{
		Func: (*IRCServer).cmdServiceAlias,
		ImmediatelyCompactable: true,
	}
	Commands["NICKSERV"] = serviceAlias
	Commands["CHANSERV"] = serviceAlias
	Commands["OPERSERV"] = serviceAlias
	Commands["MEMOSERV"] = serviceAlias
	Commands["HOSTSERV"] = serviceAlias
	Commands["BOTSERV"] = serviceAlias
	Commands["NS"] = serviceAlias
	Commands["CS"] = serviceAlias
	Commands["OS"] = serviceAlias
	Commands["MS"] = serviceAlias
	Commands["HS"] = serviceAlias
	Commands["BS"] = serviceAlias

	if os.Getenv("ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND") == "1" {
		Commands["PANIC"] = &ircCommand{
			Func: func(i *IRCServer, s *Session, reply *Replyctx, msg *irc.Message) {
				panic("PANIC called")
			},
		}
	}
	Commands["PASS"] = &ircCommand{Func: (*IRCServer).cmdPass}
}

func (i *IRCServer) cmdPing(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOORIGIN,
			Params:   []string{s.Nick},
			Trailing: "No origin specified",
		})
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.PONG,
		Params:  []string{msg.Params[0]},
	})
}

// login is called by either cmdNick or cmdUser, depending on which message the
// client sends last.
func (i *IRCServer) login(s *Session, reply *Replyctx, msg *irc.Message) {
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_WELCOME,
		Params:   []string{s.Nick},
		Trailing: "Welcome to RobustIRC!",
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_YOURHOST,
		Params:   []string{s.Nick},
		Trailing: "Your host is " + i.ServerPrefix.Name,
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_CREATED,
		Params:   []string{s.Nick},
		Trailing: "This server was created " + i.ServerCreation.UTC().String(),
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_MYINFO,
		Params:   []string{s.Nick},
		Trailing: i.ServerPrefix.Name + " v1 i nsti",
	})

	// send ISUPPORT as per:
	// http://www.irc.org/tech_docs/draft-brocklesby-irc-isupport-03.txt
	// http://www.irc.org/tech_docs/005.html
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: "005",
		Params: []string{
			"CHANTYPES=#",
			"CHANNELLEN=" + maxChannelLen,
			"NICKLEN=" + maxNickLen,
			"MODES=1",
			"PREFIX=(o)@",
			"KNOCK",
		},
		Trailing: "are supported by this server",
	})

	i.sendServices(reply, &irc.Message{
		Command: irc.NICK,
		Params: []string{
			s.Nick,
			"1", // hopcount (ignored by anope)
			"1", // timestamp
			s.Username,
			s.ircPrefix.Host,
			i.ServerPrefix.Name,
			s.svid,
			"+",
		},
		Trailing:      s.Realname,
		EmptyTrailing: true,
	})

	if pass := extractPassword(s.Pass, "nickserv"); pass != "" {
		i.sendServices(reply, &irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  irc.PRIVMSG,
			Params:   []string{"NickServ"},
			Trailing: fmt.Sprintf("IDENTIFY %s", pass),
		})
	}

	if pass := extractPassword(s.Pass, "oper"); pass != "" {
		parsed := irc.ParseMessage("OPER " + pass)
		if len(parsed.Params) > 1 {
			i.cmdOper(s, reply, parsed)
		}
	}

	i.cmdMotd(s, reply, msg)
}

func prepareStmtNick(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsNick (msgid integer not null unique primary key, session integer not null)"
		prepareStmt = "INSERT INTO paramsNick (msgid, session) VALUES (?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsNick(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsNickWin AS SELECT * FROM paramsNick WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertNick(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	_, err := stmt.Exec(msgid.Id, session.Id)
	return err
}

func compactNick(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE candidates AS
SELECT
    a.msgid AS msgid,
    a.session AS session,
    MIN(b.msgid) AS superseding_msgid
FROM
    paramsNickWin AS a
    INNER JOIN paramsNickWin AS b
    ON (
        a.session = b.session AND
        b.msgid > a.msgid
    )
    GROUP BY a.msgid;

DELETE FROM candidates WHERE msgid IN (
    SELECT
        MIN(msgid)
    FROM
        paramsNickWin
    GROUP BY session
);
DELETE FROM candidates WHERE msgid IN (
    SELECT
        c.msgid
    FROM
        candidates AS c
        INNER JOIN paramsTopicWin AS t
        ON (
            c.session = t.session AND
            t.msgid > c.msgid AND
            t.msgid < c.superseding_msgid
        )
);

-- sqlite3 cannot drop columns in ALTER TABLE statements, so we need to copy.
CREATE TEMPORARY TABLE deleteIds AS SELECT msgid FROM candidates;
DROP TABLE candidates;

-- Delete all NICK messages which are preceded by createSession and followed by deleteSession or QUIT
INSERT INTO deleteIds SELECT
    a.msgid AS msgid
FROM
    (
        SELECT
            next.msgid AS msgid,
            next.session AS session,
            next.next_msgid AS next_msgid,
            MAX(a.msgid) AS prev_msgid
        FROM
            (
                SELECT
                    n.msgid AS msgid,
                    n.session AS session,
                    MIN(a.msgid) AS next_msgid
                FROM
                    paramsNickWin AS n
                    INNER JOIN allMessagesWin AS a
                    ON (
                        n.session = a.session AND
                        a.msgid > n.msgid
                    )
                GROUP BY n.msgid
            ) AS next
            INNER JOIN allMessagesWin AS a
            ON (
                next.session = a.session AND
                a.msgid < next.msgid
            )
            GROUP BY next.msgid
    ) AS a
	INNER JOIN (
        SELECT msgid, session FROM deleteSessionWin
        UNION SELECT msgid, session FROM paramsQuitWin
    ) AS dq
	ON (
        a.session = dq.session AND
        a.next_msgid = dq.msgid
    )
	INNER JOIN createSessionWin AS c
	ON (
        a.session = c.session AND
        a.prev_msgid = c.msgid
    );

DELETE FROM paramsNick WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdNick(s *Session, reply *Replyctx, msg *irc.Message) {
	oldPrefix := s.ircPrefix

	var nick string
	if len(msg.Params) >= 1 {
		nick = msg.Params[0]
	} else {
		nick = strings.TrimSpace(msg.Trailing)
	}

	if nick == "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NONICKNAMEGIVEN,
			Trailing: "No nickname given",
		})
		return
	}

	dest := "*"
	onlyCapsChanged := false // Whether the nick change only changes capitalization.
	if s.loggedIn() {
		dest = s.Nick
		onlyCapsChanged = NickToLower(nick) == NickToLower(dest)
	}

	if !IsValidNickname(nick) {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_ERRONEUSNICKNAME,
			Params:   []string{dest, nick},
			Trailing: "Erroneous nickname",
		})
		return
	}

	if _, ok := i.nicks[NickToLower(nick)]; (ok && !onlyCapsChanged) || IsServicesNickname(nick) {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NICKNAMEINUSE,
			Params:   []string{dest, nick},
			Trailing: "Nickname is already in use",
		})
		return
	}

	if hold, ok := i.svsholds[NickToLower(nick)]; ok {
		if !s.LastActivity.After(hold.added.Add(hold.duration)) {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_ERRONEUSNICKNAME,
				Params:   []string{dest, nick},
				Trailing: fmt.Sprintf("Erroneous Nickname: %s", hold.reason),
			})
			return
		}
		// The SVSHOLD expired, so remove it.
		delete(i.svsholds, NickToLower(nick))
	}

	loggedIn := s.loggedIn()
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
					Prefix:   &oldPrefix,
					Command:  irc.NICK,
					Trailing: nick,
				})))
		return
	}

	if !loggedIn && s.loggedIn() {
		i.login(s, reply, msg)
	}
}

// createAndPrepare is a helper function which encapsulates the common case for
// a CompactionPrepareStmt callback: it calls db.Exec to create a new table,
// then db.Prepare for a prepared INSERT statement on that table.
func createAndPrepare(db *sql.DB, createStmt, prepareStmt string) (*sql.Stmt, error) {
	if _, err := db.Exec(createStmt); err != nil {
		return nil, err
	}
	return db.Prepare(prepareStmt)
}

func prepareStmtUser(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsUser (msgid integer not null unique primary key, session integer not null)"
		prepareStmt = "INSERT INTO paramsUser (msgid, session) VALUES (?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsUser(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsUserWin AS SELECT * FROM paramsUser WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertUser(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	_, err := stmt.Exec(msgid.Id, session.Id)
	return err
}

func compactUser(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE deleteIds AS
-- Delete all but the first USER message of each session.
SELECT 
    a.msgid AS msgid
FROM
    paramsUserWin AS a
    INNER JOIN paramsUserWin AS b
    ON (
        a.session = b.session AND
        b.msgid < a.msgid
    )

-- Delete all USER messages which are directly followed by a QUIT message.
UNION SELECT
    a.msgid AS msgid
FROM
    (
        SELECT
            u.msgid AS msgid,
            u.session AS session,
            MIN(a.msgid) AS next_msgid
        FROM
            paramsUserWin AS u
            INNER JOIN allMessagesWin AS a
            ON (
                u.session = a.session AND
                a.msgid > u.msgid
            )
		GROUP BY u.msgid
    ) AS a
    INNER JOIN paramsQuitWin AS q
    ON (
        a.session = q.session AND
        a.next_msgid = q.msgid
    )

-- Delete all USER messages which are directly followed by a deleteSession message.
UNION SELECT
    a.msgid AS msgid
FROM
    (
        SELECT
            u.msgid AS msgid,
            u.session AS session,
            MIN(a.msgid) AS next_msgid
        FROM
            paramsUserWin AS u
            INNER JOIN allMessagesWin AS a
            ON (
                u.session = a.session AND
                a.msgid > u.msgid
            )
        GROUP BY u.msgid
    ) AS a
    INNER JOIN deleteSessionWin AS d
    ON (
        a.session = d.session AND
        a.next_msgid = d.msgid
    );

DELETE FROM paramsUser WHERE msgid IN (SELECT msgid FROM deleteIds)
	`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdUser(s *Session, reply *Replyctx, msg *irc.Message) {
	loggedIn := s.loggedIn()
	// We keep the username (so that bans are more effective) and realname
	// (some people actually set it and look at it).
	s.Username = msg.Params[0]
	s.Realname = msg.Trailing
	s.updateIrcPrefix()
	if !loggedIn && s.loggedIn() {
		i.login(s, reply, msg)
	}
}

func prepareStmtJoin(db *sql.DB) (*sql.Stmt, error) {
	const (
		// We cannot make msgid unique because one JOIN message may contain
		// multiple channels.
		createStmt = `
		CREATE TABLE paramsJoin (msgid integer not null, session integer not null, channel text not null);
		CREATE INDEX paramsJoinMsgid ON paramsJoin (msgid);
		CREATE INDEX paramsJoinSession ON paramsJoin (session);
		`
		prepareStmt = "INSERT INTO paramsJoin (msgid, session, channel) VALUES (?, ?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsJoin(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsJoinWin AS SELECT * FROM paramsJoin WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertJoin(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	for _, channelname := range strings.Split(ircmsg.Params[0], ",") {
		if _, err := stmt.Exec(msgid.Id, session.Id, strings.ToLower(channelname)); err != nil {
			return err
		}
	}
	return nil
}

func compactJoin(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE candidates AS
SELECT
    j.msgid AS join_msgid,
    j.session AS session,
    j.channel AS channel,
    p.msgid AS part_msgid
FROM
    paramsJoinWin AS j
    LEFT JOIN paramsPartWin AS p
    ON (
        j.session = p.session AND
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
   j.channel AS channel,
   d.msgid AS part_msgid
FROM
   (
		SELECT
			js.msgid AS msgid,
			js.session AS session,
			js.channel AS channel,
			MIN(a.msgid) AS next_msgid
		FROM
			paramsJoinWin AS js
			INNER JOIN allMessagesWin AS a
			ON (
				js.session = a.session AND
				(a.irccommand != 'JOIN' OR a.irccommand IS NULL) AND
				a.msgid > js.msgid
			)
		GROUP BY js.msgid
	) AS j
	INNER JOIN deleteSessionWin AS d
	ON (
		j.session = d.session AND
		j.next_msgid = d.msgid
	);

DELETE FROM
    candidates
WHERE
    join_msgid IN (
        SELECT
            join_msgid
        FROM
            candidates AS c
            INNER join paramsTopicWin AS t
            ON (
                t.msgid > c.join_msgid AND
                t.msgid < c.part_msgid AND
                t.channel = c.channel AND
                t.session = c.session
            )
    );

DELETE FROM
    candidates
WHERE
    join_msgid IN (
        SELECT
            msgid
        FROM
            paramsJoinWin AS j
            LEFT JOIN candidates AS c
            ON (
                j.msgid = c.join_msgid AND
                j.channel = c.channel
            )
        WHERE
            c.join_msgid is null
    );

-- sqlite3 cannot drop columns in ALTER TABLE statements, so we need to copy.
CREATE TEMPORARY TABLE deleteIds AS SELECT join_msgid AS msgid FROM candidates;
DROP TABLE candidates;

DELETE FROM paramsJoin WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdJoin(s *Session, reply *Replyctx, msg *irc.Message) {
	for _, channelname := range strings.Split(msg.Params[0], ",") {
		if !IsValidChannel(channelname) {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "No such channel",
			})
			continue
		}
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			c = &channel{
				name:  channelname,
				nicks: make(map[lcNick]*[maxChanMemberStatus]bool),
			}
			i.channels[ChanToLower(channelname)] = c
		} else if c.modes['i'] && !s.invitedTo[ChanToLower(channelname)] {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_INVITEONLYCHAN,
				Params:   []string{s.Nick, c.name},
				Trailing: "Cannot join channel (+i)",
			})
			continue
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
			Prefix:   &s.ircPrefix,
			Command:  irc.JOIN,
			Trailing: channelname,
		})
		var prefix string
		if c.nicks[NickToLower(s.Nick)][chanop] {
			prefix = prefix + string('@')
		}
		i.sendServices(reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  "SJOIN",
			Params:   []string{"1", channelname},
			Trailing: prefix + s.Nick,
		})
		// Integrate the topic response by simulating a TOPIC command.
		i.cmdTopic(s, reply, &irc.Message{Command: irc.TOPIC, Params: []string{channelname}})
		i.cmdNames(s, reply, &irc.Message{Command: irc.NAMES, Params: []string{channelname}})
	}
}

func (i *IRCServer) cmdKick(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "No such nick/channel",
		})
		return
	}

	perms, ok := c.nicks[NickToLower(s.Nick)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, channelname},
			Trailing: "You're not on that channel",
		})
		return
	}

	if !perms[chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, channelname},
			Trailing: "You're not channel operator",
		})
		return
	}

	if _, ok := c.nicks[NickToLower(msg.Params[1])]; !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_USERNOTINCHANNEL,
			Params:   []string{s.Nick, msg.Params[1], channelname},
			Trailing: "They aren't on that channel",
		})
		return
	}

	// Must exist since c.nicks contains the nick.
	session, _ := i.nicks[NickToLower(msg.Params[1])]

	i.sendServices(reply,
		i.sendChannel(c, reply, &irc.Message{
			Prefix:        &s.ircPrefix,
			Command:       irc.KICK,
			Params:        []string{msg.Params[0], msg.Params[1]},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		}))

	// TODO(secure): reduce code duplication with cmdPart()
	delete(c.nicks, NickToLower(msg.Params[1]))
	i.maybeDeleteChannel(c)
	delete(session.Channels, ChanToLower(channelname))
}

func prepareStmtPart(db *sql.DB) (*sql.Stmt, error) {
	const (
		// We cannot make msgid unique because one JOIN message may contain
		// multiple channels.
		createStmt = `
		CREATE TABLE paramsPart (msgid integer not null, session integer not null, channel text not null);
		CREATE INDEX paramsPartMsgid ON paramsPart (msgid);
		CREATE INDEX paramsPartSession ON paramsPart (session);
		`
		prepareStmt = "INSERT INTO paramsPart (msgid, session, channel) VALUES (?, ?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsPart(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsPartWin AS SELECT * FROM paramsPart WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertPart(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	for _, channelname := range strings.Split(ircmsg.Params[0], ",") {
		if _, err := stmt.Exec(msgid.Id, session.Id, strings.ToLower(channelname)); err != nil {
			return err
		}
	}
	return nil
}

func compactPart(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE deleteIds AS
SELECT
    p.msgid AS msgid
FROM
    paramsPartWin AS p
    LEFT JOIN paramsJoinWin AS j
    ON (
        p.session = j.session AND
        p.msgid > j.msgid AND
        p.channel = j.channel
    )
GROUP BY p.msgid
HAVING COUNT(j.msgid) = 0;

DELETE FROM paramsPart WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdPart(s *Session, reply *Replyctx, msg *irc.Message) {
	for _, channelname := range strings.Split(msg.Params[0], ",") {
		c, ok := i.channels[ChanToLower(channelname)]
		if !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "No such channel",
			})
			continue
		}

		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOTONCHANNEL,
				Params:   []string{s.Nick, channelname},
				Trailing: "You're not on that channel",
			})
			continue
		}

		i.sendServices(reply,
			i.sendChannel(c, reply, &irc.Message{
				Prefix:  &s.ircPrefix,
				Command: irc.PART,
				Params:  []string{channelname},
			}))

		delete(c.nicks, NickToLower(s.Nick))
		i.maybeDeleteChannel(c)
		delete(s.Channels, ChanToLower(channelname))
	}
}

func prepareStmtQuit(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsQuit (msgid integer not null unique primary key, session integer not null)"
		prepareStmt = "INSERT INTO paramsQuit (msgid, session) VALUES (?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsQuit(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsQuitWin AS SELECT * FROM paramsQuit WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertQuit(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	_, err := stmt.Exec(msgid.Id, session.Id)
	return err
}

func compactQuit(db *sql.DB) error {
	const query = `
-- Delete all QUIT messages immediately preceded by a createSession message.
CREATE TEMPORARY TABLE deleteIds AS
SELECT
    a.msgid AS msgid
FROM
    (
        SELECT
            q.msgid AS msgid,
            q.session AS session,
            MAX(a.msgid) AS next_msgid
        FROM
            paramsQuitWin AS q
            INNER JOIN allMessagesWin AS a
            ON (
                q.session = a.session AND
                a.msgid < q.msgid
            )
        GROUP BY q.msgid
    ) AS a
    INNER JOIN createSessionWin AS c
    ON (
        a.session = c.session AND
        a.next_msgid = c.msgid
    );

DELETE FROM paramsQuit WHERE msgid IN (SELECT msgid FROM deleteIds);

-- Delete all QUIT messages which are directly followed by a deleteSession message.
INSERT INTO deleteIds
SELECT
    a.msgid AS msgid
FROM
    (
        SELECT
            q.msgid AS msgid,
            q.session AS session,
            MIN(a.msgid) AS next_msgid
        FROM
            paramsQuitWin AS q
            INNER JOIN allMessagesWin AS a
            ON (
                q.session = a.session AND
                a.msgid > q.msgid
            )
        GROUP BY q.msgid
    ) AS a
    INNER JOIN deleteSessionWin AS d
    ON (
        a.session = d.session AND
        a.next_msgid = d.msgid
    );
DELETE FROM paramsQuit WHERE msgid IN (SELECT msgid FROM deleteIds);
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdQuit(s *Session, reply *Replyctx, msg *irc.Message) {
	i.DeleteSession(s)
	if s.loggedIn() {
		i.sendServices(reply,
			i.sendCommonChannels(s, reply, &irc.Message{
				Prefix:        &s.ircPrefix,
				Command:       irc.QUIT,
				Trailing:      msg.Trailing,
				EmptyTrailing: true,
			}))
		i.sendUser(s, reply, &irc.Message{
			Command:  irc.ERROR,
			Trailing: fmt.Sprintf("Closing Link: %s[%s] (%s)", s.Nick, s.ircPrefix.Host, msg.Trailing),
		})
	}
}

func (i *IRCServer) cmdPrivmsg(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NORECIPIENT,
			Params:   []string{s.Nick},
			Trailing: "No recipient given (PRIVMSG)",
		})
		return
	}

	if msg.Trailing == "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTEXTTOSEND,
			Params:   []string{s.Nick},
			Trailing: "No text to send",
		})
		return
	}

	if strings.HasPrefix(msg.Params[0], "#") {
		c, ok := i.channels[ChanToLower(msg.Params[0])]
		if !ok {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_NOSUCHCHANNEL,
				Params:   []string{s.Nick, msg.Params[0]},
				Trailing: "No such channel",
			})
			return
		}
		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok && c.modes['n'] {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.ERR_CANNOTSENDTOCHAN,
				Params:   []string{s.Nick, c.name},
				Trailing: "Cannot send to channel",
			})
			return
		}
		i.sendChannelButOne(c, s, reply, &irc.Message{
			Prefix:        &s.ircPrefix,
			Command:       msg.Command,
			Params:        []string{msg.Params[0]},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	i.sendUser(session, reply, &irc.Message{
		Prefix:        &s.ircPrefix,
		Command:       msg.Command,
		Params:        []string{msg.Params[0]},
		Trailing:      msg.Trailing,
		EmptyTrailing: true,
	})

	if session.AwayMsg != "" && msg.Command == irc.PRIVMSG {
		i.sendUser(s, reply, &irc.Message{
			Prefix:        i.ServerPrefix,
			Command:       irc.RPL_AWAY,
			Params:        []string{s.Nick, msg.Params[0]},
			Trailing:      session.AwayMsg,
			EmptyTrailing: true,
		})
	}
}

func prepareStmtMode(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsMode (msgid integer not null unique primary key, session integer not null, channel text not null, modestr text)"
		prepareStmt = "INSERT INTO paramsMode (msgid, session, channel, modestr) VALUES (?, ?, ?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsMode(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsModeWin AS SELECT * FROM paramsMode WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertMode(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	var modestr string
	if len(ircmsg.Params) > 1 {
		modestr = ircmsg.Params[1]
	}
	_, err := stmt.Exec(msgid.Id, session.Id, ircmsg.Params[0],
		sql.NullString{
			String: modestr,
			Valid:  modestr != ""})
	return err
}

func compactMode(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE deleteIds AS
SELECT
    msgid
FROM
    paramsModeWin
WHERE
	modestr IS NULL;
DELETE FROM paramsMode WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdMode(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	// TODO(secure): properly distinguish between users and channels
	if s.Channels[ChanToLower(channelname)] {
		// Channel must exist, the user is in it.
		c := i.channels[ChanToLower(channelname)]
		var modestr string
		if len(msg.Params) > 1 {
			modestr = msg.Params[1]
		}
		// TODO(secure): this is special cased for now. The behavior in
		// UnrealIRCD is to silently ignore query-modes (like b) when combined
		// with any other mode, even if it’s another query mode (like e).
		if modestr == "+b" {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.RPL_ENDOFBANLIST,
				Params:   []string{s.Nick, channelname},
				Trailing: "End of Channel Ban List",
			})
			return
		}
		if strings.HasPrefix(modestr, "+") || strings.HasPrefix(modestr, "-") {
			if !c.nicks[NickToLower(s.Nick)][chanop] && !s.Operator {
				i.sendUser(s, reply, &irc.Message{
					Prefix:   i.ServerPrefix,
					Command:  irc.ERR_CHANOPRIVSNEEDED,
					Params:   []string{s.Nick, channelname},
					Trailing: "You're not channel operator",
				})
				return
			}
			// true for adding a mode, false for removing it
			newvalue := strings.HasPrefix(modestr, "+")
			modearg := 2
			for _, char := range modestr[1:] {
				switch char {
				case '+', '-':
					newvalue = (char == '+')
				case 't', 's', 'i', 'n':
					c.modes[char] = newvalue
				case 'o':
					if len(msg.Params) > modearg {
						nick := msg.Params[modearg]
						perms, ok := c.nicks[NickToLower(nick)]
						if !ok {
							i.sendUser(s, reply, &irc.Message{
								Prefix:   i.ServerPrefix,
								Command:  irc.ERR_USERNOTINCHANNEL,
								Params:   []string{s.Nick, nick, channelname},
								Trailing: "They aren't on that channel",
							})
						} else {
							// If the user already is a chanop, silently do
							// nothing (like UnrealIRCd).
							if perms[chanop] != newvalue {
								c.nicks[NickToLower(nick)][chanop] = newvalue
							}
						}
					}
					modearg++
				default:
					i.sendUser(s, reply, &irc.Message{
						Prefix:   i.ServerPrefix,
						Command:  irc.ERR_UNKNOWNMODE,
						Params:   []string{s.Nick, string(char)},
						Trailing: "is unknown mode char to me",
					})
				}
			}
			if reply.replyid > 0 {
				// TODO(secure): see how other ircds are handling this. do they sanity check the entire mode string before applying it, or do they keep valid modes while erroring for others?
				return
			}
			i.sendServices(reply,
				i.sendChannel(c, reply, &irc.Message{
					Prefix:  &s.ircPrefix,
					Command: irc.MODE,
					Params:  msg.Params[:modearg],
				}))
			return
		}
		if len(msg.Params) > 1 && msg.Params[1] == "b" {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.RPL_ENDOFBANLIST,
				Params:   []string{s.Nick, channelname},
				Trailing: "End of Channel Ban List",
			})
			return
		}
		modestr = "+"
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
	if NickToLower(channelname) == NickToLower(s.Nick) {
		modestr := "+"
		for mode := 'A'; mode < 'z'; mode++ {
			if s.modes[mode] {
				modestr += string(mode)
			}
		}
		i.sendServices(reply,
			i.sendUser(s, reply, &irc.Message{
				Prefix:   &s.ircPrefix,
				Command:  irc.MODE,
				Params:   []string{s.Nick},
				Trailing: modestr,
			}))
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.ERR_NOTONCHANNEL,
		Params:   []string{s.Nick, channelname},
		Trailing: "You're not on that channel",
	})
	return
}

func (i *IRCServer) cmdWho(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) < 1 {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_ENDOFWHO,
			Params:   []string{s.Nick},
			Trailing: "End of /WHO list",
		})
		return
	}

	// TODO: support WHO on nicknames
	channelname := msg.Params[0]

	lastmsg := &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_ENDOFWHO,
		Params:   []string{s.Nick, channelname},
		Trailing: "End of /WHO list",
	}

	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, lastmsg)
		return
	}

	if c.modes['s'] {
		if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
			i.sendUser(s, reply, lastmsg)
			return
		}
	}

	nicks := make([]string, 0, len(c.nicks))
	for nick := range c.nicks {
		nicks = append(nicks, i.nicks[nick].Nick)
	}

	sort.Strings(nicks)

	for _, nick := range nicks {
		session := i.nicks[NickToLower(nick)]
		prefix := session.ircPrefix
		// TODO: also list all other usermodes
		goneStatus := "H"
		if session.AwayMsg != "" {
			goneStatus = "G"
		}
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_WHOREPLY,
			Params:   []string{s.Nick, channelname, prefix.User, prefix.Host, i.ServerPrefix.Name, prefix.Name, goneStatus},
			Trailing: "0 " + session.Realname,
		})
	}

	i.sendUser(s, reply, lastmsg)
}

func (i *IRCServer) cmdOper(s *Session, reply *Replyctx, msg *irc.Message) {
	name := msg.Params[0]
	password := msg.Params[1]
	authenticated := false
	for _, op := range i.Config.Operators {
		if op.Name == name && op.Password == password {
			authenticated = true
			break
		}
	}

	if !authenticated {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_PASSWDMISMATCH,
			Params:   []string{s.Nick},
			Trailing: "Password incorrect",
		})
		return
	}

	s.Operator = true
	s.modes['o'] = true

	modestr := "+"
	for mode := 'A'; mode < 'z'; mode++ {
		if s.modes[mode] {
			modestr += string(mode)
		}
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_YOUREOPER,
		Params:   []string{s.Nick},
		Trailing: "You are now an IRC operator",
	})
	i.sendServices(reply,
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.MODE,
			Params:   []string{s.Nick},
			Trailing: modestr,
		}))
}

func (i *IRCServer) cmdKill(s *Session, reply *Replyctx, msg *irc.Message) {
	if strings.TrimSpace(msg.Trailing) == "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NEEDMOREPARAMS,
			Params:   []string{s.Nick, msg.Command},
			Trailing: "Not enough parameters",
		})
		return
	}

	if !s.Operator {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOPRIVILEGES,
			Params:   []string{s.Nick},
			Trailing: "Permission Denied - You're not an IRC operator",
		})
		return
	}

	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	i.DeleteSession(session)

	i.sendServices(reply,
		i.sendCommonChannels(session, reply, &irc.Message{
			Prefix:   &session.ircPrefix,
			Command:  irc.QUIT,
			Trailing: "Killed by " + s.Nick + ": " + msg.Trailing,
		}))

	i.sendUser(session, reply, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.KILL,
		Params:   []string{session.Nick},
		Trailing: fmt.Sprintf("ircd!%s!%s (%s)", s.ircPrefix.Host, s.Nick, msg.Trailing),
	})

	i.sendUser(session, reply, &irc.Message{
		Command:  irc.ERROR,
		Trailing: fmt.Sprintf("Closing Link: %s[%s] (Killed (%s (%s)))", session.Nick, session.ircPrefix.Host, s.Nick, msg.Trailing),
	})
}

func prepareStmtAway(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsAway (msgid integer not null unique primary key, session integer not null, trailing text not null)"
		prepareStmt = "INSERT INTO paramsAway (msgid, session, trailing) VALUES (?, ?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsAway(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsAwayWin AS SELECT * FROM paramsAway WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertAway(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	_, err := stmt.Exec(msgid.Id, session.Id, ircmsg.Trailing)
	return err
}

func compactAway(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE deleteIds AS
SELECT
    msgid
FROM
    paramsAwayWin
WHERE
    msgid IN (
        SELECT
            DISTINCT a.msgid
        FROM
            paramsAwayWin AS a
            INNER JOIN paramsAwayWin AS b
            ON (
                a.session = b.session AND
                b.msgid > a.msgid
            )
    );

-- Delete all AWAY messages which are directly followed by a deleteSession message.
INSERT INTO deleteIds
SELECT
	a.msgid AS msgid
FROM
	(
		SELECT
			aw.msgid AS msgid,
			aw.session AS session,
			MIN(a.msgid) AS next_msgid
		FROM
			paramsAwayWin AS aw
			INNER JOIN allMessagesWin AS a
			ON (
				aw.session = a.session AND
				a.msgid > aw.msgid
			)
		GROUP BY aw.msgid
	) AS a
	INNER JOIN deleteSessionWin AS d
	ON (
		a.session = d.session AND
		a.next_msgid = d.msgid
	);

DELETE FROM paramsAway WHERE msgid IN (SELECT msgid FROM deleteIds);

-- Delete all remaining AWAY commands that are no-ops.
INSERT INTO deleteIds SELECT msgid FROM paramsAwayWin WHERE trailing = '';
DELETE FROM paramsAway WHERE msgid IN (SELECT msgid FROM deleteIds);
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdAway(s *Session, reply *Replyctx, msg *irc.Message) {
	s.AwayMsg = strings.TrimSpace(msg.Trailing)
	if s.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_NOWAWAY,
			Params:   []string{s.Nick},
			Trailing: "You have been marked as being away",
		})
		return
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_UNAWAY,
		Params:   []string{s.Nick},
		Trailing: "You are no longer marked as being away",
	})
}

func prepareStmtTopic(db *sql.DB) (*sql.Stmt, error) {
	const (
		createStmt  = "CREATE TABLE paramsTopic (msgid integer not null unique primary key, session integer not null, channel text not null, trailing text)"
		prepareStmt = "INSERT INTO paramsTopic (msgid, session, channel, trailing) VALUES (?, ?, ?, ?)"
	)
	return createAndPrepare(db, createStmt, prepareStmt)
}

func prepareViewsTopic(db *sql.DB, compactionEnd time.Time) error {
	_, err := db.Exec(
		fmt.Sprintf("CREATE VIEW paramsTopicWin AS SELECT * FROM paramsTopic WHERE msgid < %d",
			compactionEnd.UnixNano()))
	return err
}

func insertTopic(msgid, session types.RobustId, ircmsg *irc.Message, stmt *sql.Stmt) error {
	if len(ircmsg.Params) < 1 {
		return nil
	}
	_, err := stmt.Exec(msgid.Id, session.Id, ircmsg.Params[0],
		sql.NullString{
			String: ircmsg.Trailing,
			Valid:  ircmsg.Trailing != "" || ircmsg.EmptyTrailing})
	return err
}

func compactTopic(db *sql.DB) error {
	const query = `
CREATE TEMPORARY TABLE deleteIds AS
SELECT
    msgid
FROM
    paramsTopicWin
WHERE
    msgid NOT IN (
        SELECT
            MAX(msgid)
        FROM
            paramsTopicWin
        GROUP BY channel
    )
	OR trailing IS NULL;
DELETE FROM paramsTopic WHERE msgid IN (SELECT msgid FROM deleteIds)
`

	_, err := db.Exec(query)
	return err
}

func (i *IRCServer) cmdTopic(s *Session, reply *Replyctx, msg *irc.Message) {
	channel := msg.Params[0]
	c, ok := i.channels[ChanToLower(channel)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHCHANNEL,
			Params:   []string{s.Nick, channel},
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
			Prefix:        &s.ircPrefix,
			Command:       irc.TOPIC,
			Params:        []string{channel},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		i.sendServices(reply, &irc.Message{
			Prefix:        &irc.Prefix{Name: s.Nick},
			Command:       irc.TOPIC,
			Params:        []string{channel, s.Nick, "0"},
			Trailing:      msg.Trailing,
			EmptyTrailing: true,
		})
		return
	}

	if !s.Channels[ChanToLower(channel)] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, channel},
			Trailing: "You're not on that channel",
		})
		return
	}

	// “TOPIC”, i.e. get the topic.
	if msg.Trailing == "" {
		if c.topicTime.IsZero() {
			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.RPL_NOTOPIC,
				Params:   []string{s.Nick, channel},
				Trailing: "No topic is set",
			})
			return
		}

		// TODO(secure): if the channel is secret, return ERR_NOTONCHANNEL

		i.sendUser(s, reply, &irc.Message{
			Prefix:        i.ServerPrefix,
			Command:       irc.RPL_TOPIC,
			Params:        []string{s.Nick, channel},
			Trailing:      c.topic,
			EmptyTrailing: true,
		})
		i.sendUser(s, reply, &irc.Message{
			Prefix: i.ServerPrefix,
			// RPL_TOPICWHOTIME (ircu-specific, not in the RFC)
			Command: "333",
			Params:  []string{s.Nick, channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10)},
		})
		return
	}

	if c.modes['t'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, channel},
			Trailing: "You're not channel operator",
		})
		return
	}

	c.topicNick = s.Nick
	c.topicTime = s.LastActivity
	c.topic = msg.Trailing

	i.sendChannel(c, reply, &irc.Message{
		Prefix:   &s.ircPrefix,
		Command:  irc.TOPIC,
		Params:   []string{channel},
		Trailing: msg.Trailing,
	})
	i.sendServices(reply, &irc.Message{
		Prefix:   &irc.Prefix{Name: s.Nick},
		Command:  irc.TOPIC,
		Params:   []string{channel, c.topicNick, strconv.FormatInt(c.topicTime.Unix(), 10)},
		Trailing: msg.Trailing,
	})
}

func (i *IRCServer) cmdMotd(s *Session, reply *Replyctx, msg *irc.Message) {
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_MOTDSTART,
		Params:   []string{s.Nick},
		Trailing: "- " + i.ServerPrefix.Name + " Message of the day -",
	})
	// TODO(secure): make motd configurable
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_MOTD,
		Params:   []string{s.Nick},
		Trailing: "- No MOTD configured yet.",
	})
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_ENDOFMOTD,
		Params:   []string{s.Nick},
		Trailing: "End of MOTD command",
	})
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
	} else {
		s.Pass = msg.Trailing
	}
	if !strings.HasPrefix(s.Pass, "nickserv=") &&
		!strings.HasPrefix(s.Pass, "services=") &&
		!strings.HasPrefix(s.Pass, "network=") &&
		!strings.HasPrefix(s.Pass, "oper=") &&
		!strings.HasPrefix(s.Pass, "session=") {
		s.Pass = "nickserv=" + s.Pass
	}
}

func (i *IRCServer) cmdWhois(s *Session, reply *Replyctx, msg *irc.Message) {
	session, ok := i.nicks[NickToLower(msg.Params[0])]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:        i.ServerPrefix,
		Command:       irc.RPL_WHOISUSER,
		Params:        []string{s.Nick, session.Nick, session.ircPrefix.User, session.ircPrefix.Host, "*"},
		Trailing:      session.Realname,
		EmptyTrailing: true,
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
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_WHOISCHANNELS,
			Params:   []string{s.Nick, session.Nick},
			Trailing: strings.Join(channels, " "),
		})
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_WHOISSERVER,
		Params:   []string{s.Nick, session.Nick, i.ServerPrefix.Name},
		Trailing: "RobustIRC",
	})

	if session.Operator {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_WHOISOPERATOR,
			Params:   []string{s.Nick, session.Nick},
			Trailing: "is an IRC operator",
		})
	}

	if session.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:        i.ServerPrefix,
			Command:       irc.RPL_AWAY,
			Params:        []string{s.Nick, session.Nick},
			Trailing:      session.AwayMsg,
			EmptyTrailing: true,
		})
	}

	idle := strconv.FormatInt(int64(s.LastActivity.Sub(session.LastActivity).Seconds()), 10)
	signon := strconv.FormatInt(time.Unix(0, session.Id.Id).Unix(), 10)
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_WHOISIDLE,
		Params:   []string{s.Nick, session.Nick, idle, signon},
		Trailing: "seconds idle, signon time",
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_ENDOFWHOIS,
		Params:   []string{s.Nick, session.Nick},
		Trailing: "End of /WHOIS list",
	})
}

func (i *IRCServer) cmdList(s *Session, reply *Replyctx, msg *irc.Message) {
	channels := make([]string, 0, len(i.channels))
	if len(msg.Params) > 0 {
		for _, channel := range strings.Split(msg.Params[0], ",") {
			channelname := ChanToLower(strings.TrimSpace(channel))
			if _, ok := i.channels[channelname]; ok {
				channels = append(channels, string(channelname))
			}
		}
	} else {
		for channel := range i.channels {
			channels = append(channels, string(channel))
		}
		sort.Strings(channels)
	}
	for _, channel := range channels {
		c := i.channels[lcChan(channel)]
		if c.modes['s'] && !s.Operator && !s.Channels[lcChan(channel)] {
			continue
		}
		i.sendUser(s, reply, &irc.Message{
			Prefix:        i.ServerPrefix,
			Command:       irc.RPL_LIST,
			Params:        []string{s.Nick, c.name, strconv.Itoa(len(c.nicks))},
			Trailing:      c.topic,
			EmptyTrailing: c.topic == "",
		})
	}

	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_LISTEND,
		Params:   []string{s.Nick},
		Trailing: "End of LIST",
	})
}

func (i *IRCServer) cmdInvite(s *Session, reply *Replyctx, msg *irc.Message) {
	nickname := msg.Params[0]
	channelname := msg.Params[1]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, msg.Params[1]},
			Trailing: "You're not on that channel",
		})
		return
	}
	if _, ok := c.nicks[NickToLower(s.Nick)]; !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOTONCHANNEL,
			Params:   []string{s.Nick, msg.Params[1]},
			Trailing: "You're not on that channel",
		})
		return
	}
	session, ok := i.nicks[NickToLower(nickname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_NOSUCHNICK,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: "No such nick/channel",
		})
		return
	}
	if _, ok := c.nicks[NickToLower(nickname)]; ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_USERONCHANNEL,
			Params:   []string{s.Nick, session.Nick, c.name},
			Trailing: "is already on channel",
		})
		return
	}
	if c.modes['i'] && !c.nicks[NickToLower(s.Nick)][chanop] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.ERR_CHANOPRIVSNEEDED,
			Params:   []string{s.Nick, c.name},
			Trailing: "You're not channel operator",
		})
		return
	}
	session.invitedTo[ChanToLower(channelname)] = true
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_INVITING,
		Params:  []string{s.Nick, session.Nick, c.name},
	})
	i.sendServices(reply,
		i.sendUser(session, reply, &irc.Message{
			Prefix:   &s.ircPrefix,
			Command:  irc.INVITE,
			Params:   []string{session.Nick},
			Trailing: c.name,
		}))
	i.sendChannel(c, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.NOTICE,
		Params:   []string{c.name},
		Trailing: fmt.Sprintf("%s invited %s into the channel.", s.Nick, msg.Params[0]),
	})

	if session.AwayMsg != "" {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  irc.RPL_AWAY,
			Params:   []string{s.Nick, msg.Params[0]},
			Trailing: session.AwayMsg,
		})
	}
}

func (i *IRCServer) cmdUserhost(s *Session, reply *Replyctx, msg *irc.Message) {
	var userhosts []string
	for _, nickname := range msg.Params {
		session, ok := i.nicks[NickToLower(nickname)]
		if !ok {
			continue
		}
		awayPrefix := "+"
		if session.AwayMsg != "" {
			awayPrefix = "-"
		}
		nick := session.Nick
		if session.Operator {
			nick = nick + "*"
		}
		userhosts = append(userhosts, fmt.Sprintf("%s=%s%s", nick, awayPrefix, session.ircPrefix.String()))
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:        i.ServerPrefix,
		Command:       irc.RPL_USERHOST,
		Params:        []string{s.Nick},
		Trailing:      strings.Join(userhosts, " "),
		EmptyTrailing: len(userhosts) == 0,
	})
}

func (i *IRCServer) cmdServiceAlias(s *Session, reply *Replyctx, msg *irc.Message) {
	aliases := map[string]string{
		"NICKSERV": "PRIVMSG NickServ :",
		"NS":       "PRIVMSG NickServ :",
		"CHANSERV": "PRIVMSG ChanServ :",
		"CS":       "PRIVMSG ChanServ :",
		"OPERSERV": "PRIVMSG OperServ :",
		"OS":       "PRIVMSG OperServ :",
		"MEMOSERV": "PRIVMSG MemoServ :",
		"MS":       "PRIVMSG MemoServ :",
		"HOSTSERV": "PRIVMSG HostServ :",
		"HS":       "PRIVMSG HostServ :",
		"BOTSERV":  "PRIVMSG BotServ :",
		"BS":       "PRIVMSG BotServ :",
	}
	for alias, expanded := range aliases {
		if strings.ToUpper(msg.Command) != alias {
			continue
		}
		i.cmdPrivmsg(s, reply, irc.ParseMessage(expanded+strings.Join(msg.Params, " ")))
		return
	}
}

func (i *IRCServer) cmdNames(s *Session, reply *Replyctx, msg *irc.Message) {
	if len(msg.Params) > 0 {
		channelname := msg.Params[0]
		if c, ok := i.channels[ChanToLower(channelname)]; ok {
			nicks := make([]string, 0, len(c.nicks))
			for nick, perms := range c.nicks {
				var prefix string
				if perms[chanop] {
					prefix = prefix + string('@')
				}
				nicks = append(nicks, prefix+i.nicks[nick].Nick)
			}

			sort.Strings(nicks)

			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.RPL_NAMREPLY,
				Params:   []string{s.Nick, "=", channelname},
				Trailing: strings.Join(nicks, " "),
			})

			i.sendUser(s, reply, &irc.Message{
				Prefix:   i.ServerPrefix,
				Command:  irc.RPL_ENDOFNAMES,
				Params:   []string{s.Nick, channelname},
				Trailing: "End of /NAMES list.",
			})
			return
		}
	}
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.RPL_ENDOFNAMES,
		Params:   []string{s.Nick, "*"},
		Trailing: "End of /NAMES list.",
	})
}

func (i *IRCServer) cmdKnock(s *Session, reply *Replyctx, msg *irc.Message) {
	channelname := msg.Params[0]
	c, ok := i.channels[ChanToLower(channelname)]
	if !ok {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  "480",
			Params:   []string{s.Nick},
			Trailing: fmt.Sprintf("Cannot knock on %s (Channel does not exist)", channelname),
		})
		return
	}

	if !c.modes['i'] {
		i.sendUser(s, reply, &irc.Message{
			Prefix:   i.ServerPrefix,
			Command:  "480",
			Params:   []string{s.Nick},
			Trailing: fmt.Sprintf("Cannot knock on %s (Channel is not invite only)", channelname),
		})
		return
	}

	reason := "no reason specified"
	if len(msg.Params) > 1 {
		reason = strings.Join(msg.Params[1:], " ")
	}
	if msg.Trailing != "" {
		reason = msg.Trailing
	}

	i.sendChannel(c, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.NOTICE,
		Params:   []string{c.name},
		Trailing: fmt.Sprintf("[Knock] by %s (%s)", s.ircPrefix.String(), reason),
	})
	i.sendUser(s, reply, &irc.Message{
		Prefix:   i.ServerPrefix,
		Command:  irc.NOTICE,
		Params:   []string{s.Nick},
		Trailing: fmt.Sprintf("Knocked on %s", c.name),
	})
}
