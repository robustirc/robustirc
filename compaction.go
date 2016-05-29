package main

// Generate errors.go which is used in returnedOnlyErrors() below.
//go:generate go run generrors.go

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
	"github.com/stapelberg/glog"

	_ "github.com/mattn/go-sqlite3"
)

// returnedOnlyErrors returns true if and only if |msgid| resulted in at least
// one error and not a single non-error IRC reply.
func returnedOnlyErrors(msgid types.RobustId) bool {
	vmsgs, _ := ircServer.Get(msgid)
	for _, msg := range vmsgs {
		ircmsg := irc.ParseMessage(msg.Data)
		if ircmsg == nil {
			glog.Errorf("Output message not parsable\n")
			continue
		}
		if !errorCodes[ircmsg.Command] {
			return false
		}
	}
	return len(vmsgs) > 0
}

type robustSnapshot struct {
	firstIndex            uint64
	lastIndex             uint64
	store                 *raft_store.LevelDBStore
	del                   map[uint64]types.RobustId
	servers               map[int64]bool
	idToIdx               map[int64]uint64
	compactionEnd         time.Time
	skipDeletionForCanary bool
}

func (s *robustSnapshot) Persist(sink raft.SnapshotSink) error {
	log.Printf("Filtering and writing up to %d indexes\n", s.lastIndex-s.firstIndex)

	// Get a timestamp and keep it constant, so that we only compact messages
	// older than n days from compactionStart. If we used time.Since, new
	// messages would pour into the window on every compaction round, possibly
	// making the compaction never converge.
	compactionStart := time.Now()
	log.Printf("compactionStart %s\n", compactionStart.String())
	if *canaryCompactionStart > 0 {
		compactionStart = time.Unix(0, *canaryCompactionStart)
		log.Printf("compactionStart %s (overridden with -canary_compaction_start)\n", compactionStart.String())
	}

	s.compactionEnd = compactionStart.Add(-7 * 24 * time.Hour)
	f, err := ioutil.TempFile(*raftDir, "temp-compaction.sqlite3")
	if err != nil {
		sink.Cancel()
		return err
	}
	tempfile := f.Name()
	f.Close()
	db, err := sql.Open("sqlite3", tempfile)
	if err != nil {
		sink.Cancel()
		return err
	}
	defer func() {
		db.Close()
		os.Remove(tempfile)
	}()
	if _, err := db.Exec("pragma synchronous = off"); err != nil {
		sink.Cancel()
		return err
	}
	const nonIrcCommandStmt = `
CREATE TABLE createSession (msgid integer not null unique primary key, session integer not null);
CREATE TABLE deleteSession (msgid integer not null unique primary key, session integer not null);
CREATE TABLE allMessages (msgid integer not null unique primary key, session integer not null, irccommand string null);
CREATE VIEW allMessagesWin AS SELECT * FROM allMessages WHERE msgid < %d;
CREATE INDEX allMessagesSessionIdx ON allMessages (session);
CREATE VIEW createSessionWin AS SELECT * FROM createSession WHERE msgid < %d;
CREATE VIEW deleteSessionWin AS SELECT * FROM deleteSession WHERE msgid < %d;
`
	if _, err := db.Exec(
		fmt.Sprintf(nonIrcCommandStmt,
			s.compactionEnd.UnixNano(),
			s.compactionEnd.UnixNano(),
			s.compactionEnd.UnixNano())); err != nil {
		sink.Cancel()
		return err
	}

	var allMessagesStmt, createSessionStmt, deleteSessionStmt *sql.Stmt
	allMessagesStmt, err = db.Prepare("INSERT INTO allMessages (msgid, session, irccommand) VALUES (?, ?, ?)")
	if err == nil {
		defer allMessagesStmt.Close()
		createSessionStmt, err = db.Prepare("INSERT INTO createSession (msgid, session) VALUES (?, ?)")
	}
	if err == nil {
		defer createSessionStmt.Close()
		deleteSessionStmt, err = db.Prepare("INSERT INTO deleteSession (msgid, session) VALUES (?, ?)")
	}
	if err == nil {
		defer deleteSessionStmt.Close()
	}
	if err != nil {
		sink.Cancel()
		return err
	}

	// Let each IRC command prepare their tables and prepared statements.
	stmts := make(map[string]*sql.Stmt)
	for name, cmd := range ircserver.Commands {
		if cmd.CompactionPrepareStmt == nil {
			continue
		}
		var err error
		stmts[name], err = cmd.CompactionPrepareStmt(db)
		if err == nil {
			defer stmts[name].Close()
			err = cmd.CompactionPrepareViews(db, s.compactionEnd)
		}
		if err != nil {
			sink.Cancel()
			return err
		}
	}

	// First pass: insert all messages into database
	iterator := s.store.GetBulkIterator(s.firstIndex, s.lastIndex+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		var nlog raft.Log
		if err := iterator.Error(); err != nil {
			glog.Errorf("Error while iterating through the log: %v", err)
			available = iterator.Next()
			continue
		}
		i := binary.BigEndian.Uint64(iterator.Key())
		value := iterator.Value()
		if err := json.Unmarshal(value, &nlog); err != nil {
			glog.Errorf("Skipping log entry %d because of a JSON unmarshaling error: %v", i, err)
			continue
		}
		available = iterator.Next()

		// TODO: compact raft messages as well, so that peer changes are not kept forever
		if nlog.Type != raft.LogCommand {
			continue
		}

		parsed := types.NewRobustMessageFromBytes(nlog.Data)
		if time.Unix(0, parsed.Id.Id).After(s.compactionEnd) {
			break
		}
		s.idToIdx[parsed.Id.Id] = i

		msgid := parsed.Id
		session := parsed.Session
		ircCmdNullable := sql.NullString{Valid: false}

		switch parsed.Type {
		case types.RobustMessageOfDeath:
			s.del[i] = parsed.Id
			continue

		case types.RobustConfig:
			// TODO: implement compaction for RobustConfig
			continue

		case types.RobustIRCToClient:
			fallthrough
		case types.RobustPing:
			fallthrough
		case types.RobustAny:
			glog.Errorf("Compaction: saw unexpected message type %v (log id %d, message id %v)\n", parsed.Type, i, parsed.Id)
			continue

		default:
			glog.Errorf("Compaction: saw message of unknown type %v (log id %d, message id %v)\n", parsed.Type, i, parsed.Id)
			continue

		case types.RobustCreateSession:
			session = parsed.Id
			if _, err := createSessionStmt.Exec(msgid.Id, session.Id); err != nil {
				sink.Cancel()
				return err
			}

		case types.RobustDeleteSession:
			if _, err := deleteSessionStmt.Exec(msgid.Id, session.Id); err != nil {
				sink.Cancel()
				return err
			}

		case types.RobustIRCFromClient:
			if returnedOnlyErrors(types.RobustId{Id: parsed.Id.Id}) {
				s.del[i] = parsed.Id
				continue
			}

			ircmsg := irc.ParseMessage(parsed.Data)
			if ircmsg == nil {
				s.del[i] = parsed.Id
				continue
			}
			irccmd := strings.ToUpper(ircmsg.Command)
			ircCmdNullable = sql.NullString{String: irccmd, Valid: true}

			// Kind of a hack: we need to keep track of which sessions are
			// services connections and which are not, so that we can look at
			// the correct relevant-function (e.g. server_NICK vs. NICK).
			var serverPrefix string
			if irccmd == "SERVER" {
				s.servers[parsed.Session.Id] = true
			} else {
				if s.servers[session.Id] {
					serverPrefix = "server_"
				}
			}
			c, ok := ircserver.Commands[serverPrefix+irccmd]
			if !ok {
				// TODO: commands we don’t know should be okay to compact away, right?
				glog.Errorf("Could not find command %q", serverPrefix+irccmd)
				continue
			}
			if c.ImmediatelyCompactable {
				s.del[i] = parsed.Id
				continue
			}
			if c.CompactionInsert != nil {
				if err := c.CompactionInsert(msgid, session, ircmsg, stmts[serverPrefix+irccmd]); err != nil {
					sink.Cancel()
					return err
				}
			}
		}

		if _, err := allMessagesStmt.Exec(msgid.Id, session.Id, ircCmdNullable); err != nil {
			sink.Cancel()
			return err
		}
	}

	// We repeatedly compact, since the result of one compaction can affect the
	// result of other compactions (see compaction_test.go for examples).
	changed := true
	pass := 0
	for changed {
		log.Printf("Compaction pass %d\n", pass)
		pass++
		var err error
		changed, err = s.compact(db)
		if err != nil {
			sink.Cancel()
			return err
		}
	}

	log.Printf("Copying non-deleted messages into snapshot\n")

	available = iterator.First()
	for available {
		if err := iterator.Error(); err != nil {
			sink.Cancel()
			return err
		}
		i := binary.BigEndian.Uint64(iterator.Key())
		value := iterator.Value()
		if _, ok := s.del[i]; !ok {
			if _, err := sink.Write(value); err != nil {
				sink.Cancel()
				return err
			}
		}
		available = iterator.Next()
	}

	sink.Close()

	if s.skipDeletionForCanary {
		log.Printf("Skipping deletion (canary mode)\n")
		return nil
	}

	log.Printf("Deleting compacted messages from disk\n")
	for idx, id := range s.del {
		if err := ircServer.Delete(id); err != nil {
			log.Panicf("Could not delete outputstream message: %v\n", err)
		}
		s.store.DeleteRange(idx, idx)
	}

	log.Printf("Snapshot done\n")

	return nil
}

func (s *robustSnapshot) Release() {
}

// compact invokes all irc command’s |Compact| callbacks and runs the
// compaction for createSession and deleteSession messages.
func (s *robustSnapshot) compact(db *sql.DB) (bool, error) {
	changed := false

	for _, cmd := range ircserver.Commands {
		if cmd.Compact == nil {
			continue
		}
		if err := cmd.Compact(db); err != nil {
			return changed, err
		}
		c, err := s.markResultForCompaction(db)
		if err != nil {
			return changed, err
		}
		changed = changed || c
	}

	const stmt = `
-- Delete all deleteSession messages immediately preceded by a createSession
-- message (or commands in between which do not modify state of anything but
-- the session in question).
CREATE TEMPORARY TABLE candidates AS
SELECT
    a.msgid AS msgid,
    a.session AS session
FROM
    (
        SELECT
            d.msgid AS msgid,
            d.session AS session,
            MAX(a.msgid) AS next_msgid
        FROM
            (SELECT msgid, session FROM deleteSessionWin
             UNION SELECT msgid, session FROM paramsQuitWin) AS d
            INNER JOIN allMessagesWin AS a
            ON (
                d.session = a.session AND
                (a.irccommand IS NULL OR
                 (a.irccommand != 'NICK' AND
                  a.irccommand != 'PASS' AND
                  a.irccommand != 'QUIT')) AND
                a.msgid < d.msgid
            )
        GROUP BY d.msgid
    ) AS a
    INNER JOIN createSessionWin AS c
    ON (
        a.session = c.session AND
        a.next_msgid = c.msgid
    );
DELETE FROM deleteSession WHERE msgid IN (SELECT msgid FROM candidates);

INSERT INTO candidates
SELECT
    a.msgid,
    a.session
FROM
    allMessagesWin AS a
    LEFT JOIN candidates AS d
    ON (
        a.session = d.session
    )
    WHERE d.session IS NOT NULL;

CREATE TEMPORARY TABLE deleteIds AS SELECT msgid FROM candidates;
DROP TABLE candidates;
DELETE FROM createSession WHERE msgid IN (SELECT msgid FROM deleteIds);
`

	_, err := db.Exec(stmt)
	if err != nil {
		return changed, err
	}

	c, err := s.markResultForCompaction(db)
	if err != nil {
		return changed, err
	}
	return changed || c, nil
}

// markResultForCompaction updates |s.del| with all messages whose ids are in
// the deleteIds table. It then removes these messages from the allMessages
// table and drops the deleteIds table.
func (s *robustSnapshot) markResultForCompaction(db *sql.DB) (bool, error) {
	changed := false
	rows, err := db.Query("SELECT msgid FROM deleteIds")
	if err != nil {
		return changed, err
	}
	defer rows.Close()
	for rows.Next() {
		var msgid int64
		if err := rows.Scan(&msgid); err != nil {
			return changed, err
		}
		idx, ok := s.idToIdx[msgid]
		if !ok {
			return changed, fmt.Errorf("msgid=%v not in idToIdx", msgid)
		}
		s.del[idx] = types.RobustId{Id: msgid}
		changed = true
	}
	if err := rows.Err(); err != nil {
		return changed, err
	}

	if _, err := db.Exec("DELETE FROM allMessages WHERE msgid IN (SELECT msgid FROM deleteIds)"); err != nil {
		return changed, err
	}

	_, err = db.Exec("DROP TABLE deleteIds")
	return changed, nil
}
