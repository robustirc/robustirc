package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
	"github.com/stapelberg/glog"
)

var (
	compactionDurations = prometheus.NewSummary(prometheus.SummaryOpts{
		Name: "compaction_duration_milliseconds",
		Help: "A summary of compaction wall-clock time, in milliseconds.",
	})
	numDeleteSessionsWin = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "num_delete_sessions_win",
		Help: "Number of deleteSession messages in the compaction window.",
	})
)

func init() {
	prometheus.MustRegister(compactionDurations)
	prometheus.MustRegister(numDeleteSessionsWin)
}

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
		if !ircserver.ErrorCodes[ircmsg.Command] {
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

	db := ircServer.CompactionDatabase.DB

	const windows = `
CREATE VIEW allMessagesWin AS SELECT * FROM allMessages WHERE msgid < %d;
CREATE VIEW createSessionWin AS SELECT * FROM createSession WHERE msgid < %d;
CREATE VIEW deleteSessionWin AS SELECT * FROM deleteSession WHERE msgid < %d;
`

	if _, err := db.Exec(
		fmt.Sprintf(windows,
			s.compactionEnd.UnixNano(),
			s.compactionEnd.UnixNano(),
			s.compactionEnd.UnixNano())); err != nil {
		return err
	}

	for _, cmd := range ircserver.Commands {
		if cmd.CompactionPrepareViews == nil {
			continue
		}
		if err := cmd.CompactionPrepareViews(db, s.compactionEnd); err != nil {
			return err
		}
	}

	// First pass: build up s.idToIdx
	iterator := s.store.GetBulkIterator(s.firstIndex, s.lastIndex+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		var nlog raft.Log
		if err := iterator.Error(); err != nil {
			return err
		}
		i := binary.BigEndian.Uint64(iterator.Key())
		value := iterator.Value()
		if err := json.Unmarshal(value, &nlog); err != nil {
			glog.Errorf("Skipping log entry %d because of a JSON unmarshaling error: %v", i, err)
			available = iterator.Next()
			continue
		}
		available = iterator.Next()

		if nlog.Type != raft.LogCommand {
			return fmt.Errorf("nlog.Type = %d instead of LogCommand", nlog.Type)
		}

		parsed := types.NewRobustMessageFromBytes(nlog.Data)
		if time.Unix(0, parsed.Id.Id).After(s.compactionEnd) {
			break
		}
		s.idToIdx[parsed.Id.Id] = i
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

		case types.RobustIRCFromClient:
			fallthrough
		case types.RobustCreateSession:
			fallthrough
		case types.RobustDeleteSession:
		// Handled in FSM.Apply()

		default:
			glog.Errorf("Compaction: saw message of unknown type %v (log id %d, message id %v)\n", parsed.Type, i, parsed.Id)
			continue
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
			return err
		}
	}

	var deleteSessions int
	if err := db.QueryRow("SELECT COUNT(*) FROM deleteSessionWin").Scan(&deleteSessions); err != nil {
		return err
	}
	log.Printf("%d deleteSessions left in compaction window\n", deleteSessions)
	numDeleteSessionsWin.Set(float64(deleteSessions))

	if _, err := db.Exec(`
		DROP VIEW allMessagesWin;
		DROP VIEW createSessionWin;
		DROP VIEW deleteSessionWin;
		`); err != nil {
		return err
	}

	for _, cmd := range ircserver.Commands {
		if cmd.CompactionDropViews == nil {
			continue
		}
		if err := cmd.CompactionDropViews(db); err != nil {
			return err
		}
	}

	if _, err := db.Exec("VACUUM"); err != nil {
		return err
	}

	log.Printf("Copying non-deleted messages into snapshot\n")

	snapshotBytes := 0
	available = iterator.First()
	for available {
		if err := iterator.Error(); err != nil {
			return err
		}
		i := binary.BigEndian.Uint64(iterator.Key())
		value := iterator.Value()
		if _, ok := s.del[i]; !ok {
			n, err := sink.Write(value)
			if err != nil {
				return err
			}
			snapshotBytes += n
		}
		available = iterator.Next()
	}
	log.Printf("snapshot: wrote %d bytes", snapshotBytes)

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
	compactionDurations.Observe(float64((time.Now().Sub(compactionStart)).Nanoseconds() / int64(time.Millisecond)))

	return nil
}

func (s *robustSnapshot) Release() {
}

// compact invokes all irc command’s |Compact| callbacks and runs the
// compaction for createSession and deleteSession messages.
func (s *robustSnapshot) compact(db *sql.DB) (bool, error) {
	const nonModifying = `
CREATE TABLE deleteIds AS
SELECT msgid FROM allMessagesWin WHERE irccommand = 'TOPIC' AND msgid NOT IN (SELECT msgid FROM paramsTopicWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'NICK' AND msgid NOT IN (SELECT msgid FROM paramsNickWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'USER' AND msgid NOT IN (SELECT msgid FROM paramsUserWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'JOIN' AND msgid NOT IN (SELECT msgid FROM paramsJoinWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'PART' AND msgid NOT IN (SELECT msgid FROM paramsPartWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'QUIT' AND msgid NOT IN (SELECT msgid FROM paramsQuitWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'MODE' AND msgid NOT IN (SELECT msgid FROM paramsModeWin)
UNION SELECT msgid FROM allMessagesWin WHERE irccommand = 'AWAY' AND msgid NOT IN (SELECT msgid FROM paramsAwayWin);
	`
	started := time.Now()
	if _, err := db.Exec(nonModifying); err != nil {
		return false, err
	}

	changed, err := s.markResultForCompaction(db, "non-modifying", started)
	if err != nil {
		return changed, err
	}

	if _, err := db.Exec("CREATE TABLE validCommands (command text not null)"); err != nil {
		return changed, err
	}
	validCommandInsert, err := db.Prepare("INSERT INTO validCommands (command) VALUES (?)")
	if err != nil {
		return changed, err
	}
	defer validCommandInsert.Close()

	immediatelyCompactable, err := db.Prepare("CREATE TABLE deleteIds AS SELECT msgid FROM allMessagesWin WHERE irccommand = ?")
	if err != nil {
		return changed, err
	}
	defer immediatelyCompactable.Close()

	for n, cmd := range ircserver.Commands {
		if _, err := validCommandInsert.Exec(n); err != nil {
			return changed, err
		}
		started := time.Now()
		if cmd.Compact != nil {
			if err := cmd.Compact(db); err != nil {
				return changed, err
			}
		} else if cmd.ImmediatelyCompactable {
			if _, err := immediatelyCompactable.Exec(n); err != nil {
				return changed, err
			}
		} else {
			continue
		}
		c, err := s.markResultForCompaction(db, "command "+n, started)
		if err != nil {
			return changed, err
		}
		changed = changed || c
	}

	const invalidCommands = `
	CREATE TABLE deleteIds AS SELECT msgid FROM allMessagesWin WHERE irccommand NOT IN (SELECT command FROM validCommands);
	DROP TABLE validCommands;
	`
	started = time.Now()
	_, err = db.Exec(invalidCommands)
	if err != nil {
		return changed, err
	}

	c, err := s.markResultForCompaction(db, "invalid commands", started)
	if err != nil {
		return changed, err
	}

	const stmt = `
-- Delete all deleteSession messages immediately preceded by a createSession
-- message (or commands in between which do not modify state of anything but
-- the session in question).
CREATE TABLE candidates AS
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
                  a.irccommand != 'USER' AND
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

CREATE TABLE deleteIds AS SELECT msgid FROM candidates;
DROP TABLE candidates;
DELETE FROM createSession WHERE msgid IN (SELECT msgid FROM deleteIds);
`

	started = time.Now()
	_, err = db.Exec(stmt)
	if err != nil {
		return changed, err
	}

	c, err = s.markResultForCompaction(db, "deleteSession, createSession", started)
	if err != nil {
		return changed, err
	}
	return changed || c, nil
}

// markResultForCompaction updates |s.del| with all messages whose ids are in
// the deleteIds table. It then removes these messages from the allMessages
// table and drops the deleteIds table.
func (s *robustSnapshot) markResultForCompaction(db *sql.DB, label string, started time.Time) (bool, error) {
	changed := false
	rows, err := db.Query("SELECT msgid FROM deleteIds")
	if err != nil {
		return changed, err
	}
	defer rows.Close()
	deleted := 0
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
		deleted++
	}
	if err := rows.Err(); err != nil {
		return changed, err
	}
	log.Printf("%d rows deleted in %v (step %q)\n", deleted, time.Since(started), label)

	if _, err := db.Exec("DELETE FROM allMessages WHERE msgid IN (SELECT msgid FROM deleteIds)"); err != nil {
		return changed, err
	}

	_, err = db.Exec("DROP TABLE deleteIds")
	return changed, nil
}
