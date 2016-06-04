package ircserver

// Generate errors.go which is used in returnedOnlyErrors() below.
//go:generate go run generrors.go

import (
	"database/sql"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/stapelberg/glog"
)

type compactionDatabase struct {
	Name       string
	DB         *sql.DB
	Statements map[string]*sql.Stmt
}

// Close closes all prepared statements and the database itself.
func (c *compactionDatabase) Close() error {
	if c == nil {
		return nil
	}
	for _, stmt := range c.Statements {
		if err := stmt.Close(); err != nil {
			return err
		}
	}
	if err := c.DB.Close(); err != nil {
		return err
	}
	return os.Remove(c.Name)
}

// ExecStmt is a convenience wrapper around compactionDatabase which can be
// called on a nil pointer. This saves lots of conditionals in commands.go.
// ExecStmt directly calls log.Panicf() because FSM.Apply() and the IRC
// commands it calls do not have error handling plumbed through anyway, so the
// best we can do is to panic.
func (c *compactionDatabase) ExecStmt(stmt string, args ...interface{}) {
	if c == nil {
		return
	}
	const maxRetries = 50
	for i := 0; i < maxRetries; i++ {
		if _, err := c.Statements[stmt].Exec(args...); err != nil {
			if sqliteErr, ok := err.(sqlite3.Error); ok {
				if sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked {
					glog.Warningf("Database locked (%v), retry %d of %d\n", err, i, maxRetries)
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			log.Panicf("Inserting into compaction SQLite database %q: %v", c.Name, err)
		}
		break
	}
}

// initializeCompaction creates a new SQLite database and initializes it.
func initializeCompaction(raftDir string) (*compactionDatabase, error) {
	cdb := &compactionDatabase{
		Statements: make(map[string]*sql.Stmt),
	}
	f, err := ioutil.TempFile(raftDir, "permanent-compaction.sqlite3")
	if err != nil {
		return nil, err
	}
	tempfile := f.Name()
	f.Close()
	db, err := sql.Open("sqlite3", tempfile)
	if err != nil {
		return nil, err
	}
	// TODO: do we need to call db.Close() upon errors?
	if _, err := db.Exec("pragma synchronous = off"); err != nil {
		return nil, err
	}
	const nonIrcCommandStmt = `
CREATE TABLE createSession (msgid integer not null unique primary key, session integer not null);
CREATE TABLE deleteSession (msgid integer not null unique primary key, session integer not null);
CREATE TABLE allMessages (msgid integer not null unique primary key, session integer not null, irccommand string null);
CREATE INDEX allMessagesSessionIdx ON allMessages (session);
`
	if _, err := db.Exec(nonIrcCommandStmt); err != nil {
		return nil, err
	}

	cdb.Statements["_all"], err = db.Prepare("INSERT INTO allMessages (msgid, session, irccommand) VALUES (?, ?, ?)")
	if err != nil {
		return nil, err
	}

	cdb.Statements["_create"], err = db.Prepare("INSERT INTO createSession (msgid, session) VALUES (?, ?)")
	if err != nil {
		return nil, err
	}

	cdb.Statements["_delete"], err = db.Prepare("INSERT INTO deleteSession (msgid, session) VALUES (?, ?)")
	if err != nil {
		return nil, err
	}

	// Let each IRC command prepare their tables and prepared statements.
	for name, cmd := range Commands {
		if cmd.CompactionPrepareStmt == nil {
			continue
		}
		var err error
		cdb.Statements[name], err = cmd.CompactionPrepareStmt(db)
		if err != nil {
			return nil, err
		}
	}

	cdb.Name = tempfile
	cdb.DB = db
	return cdb, nil
}

func DeleteOldDatabases(tmpdir string) error {
	dir, err := os.Open(tmpdir)
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name, "permanent-compaction.sqlite3") {
			if err := os.Remove(filepath.Join(tmpdir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}
