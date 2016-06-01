package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"
	"github.com/sorcix/irc"
	"github.com/stapelberg/glog"
)

type FSM struct {
	// Used for invalidating messages of death.
	store *raft_store.LevelDBStore

	ircstore *raft_store.LevelDBStore
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	if err := fsm.ircstore.StoreLog(l); err != nil {
		log.Panicf("Could not persist message in irclogs/: %v", err)
	}

	msg := types.NewRobustMessageFromBytes(l.Data)
	glog.Infof("Apply(msg.Type=%s)\n", msg.Type)

	defer func() {
		if msg.Type == types.RobustMessageOfDeath {
			return
		}
		if r := recover(); r != nil {
			// Panics in ircserver.ProcessMessage() are a problem, since
			// they will bring down the entire raft cluster and you cannot
			// bring up any raft node anymore without deleting the entire
			// log.
			//
			// Therefore, when we panic, we invalidate the log entry in
			// question before crashing. This doesn’t fix the underlying
			// bug, i.e. an IRC message will then go unhandled, but it
			// prevents RobustIRC from dying horribly in such a situation.
			msg.Type = types.RobustMessageOfDeath
			data, err := json.Marshal(msg)
			if err != nil {
				glog.Fatalf("Could not marshal message: %v", err)
			}
			l.Data = data
			if err := fsm.store.StoreLog(l); err != nil {
				glog.Fatalf("Could not store log while marking message as message of death: %v", err)
			}
			log.Printf("Marked %+v as message of death\n", l)
			glog.Fatalf("%v", r)
		}
	}()

	ircCmdNullable := sql.NullString{Valid: false}
	sessionId := msg.Session

	switch msg.Type {
	case types.RobustMessageOfDeath:
		// To prevent the message from being accepted again.
		ircServer.UpdateLastClientMessageID(&msg)
		log.Printf("Skipped message of death.\n")

	case types.RobustCreateSession:
		ircServer.CreateSession(msg.Id, msg.Data)
		sessionId = msg.Id

		ircServer.CompactionDatabase.ExecStmt("_create", msg.Id.Id, sessionId.Id)

	case types.RobustDeleteSession:
		if _, err := ircServer.GetSession(msg.Session); err == nil {
			// TODO(secure): overwrite QUIT messages for services with an faq entry explaining that they are not robust yet.
			reply := ircServer.ProcessMessage(msg.Id, msg.Session, irc.ParseMessage("QUIT :"+string(msg.Data)))
			ircServer.SendMessages(reply, msg.Session, msg.Id.Id)

			ircServer.CompactionDatabase.ExecStmt("_delete", msg.Id.Id, sessionId.Id)
		}

	case types.RobustIRCFromClient:
		// Need to do this first, because ircserver.ProcessMessage could delete
		// the session, e.g. by using KILL or QUIT.
		if err := ircServer.UpdateLastClientMessageID(&msg); err != nil {
			log.Printf("Error updating the last message for session: %v\n", err)
		} else {
			ircmsg := irc.ParseMessage(string(msg.Data))

			s, err := ircServer.GetSession(msg.Session)
			var serverPrefix string
			if err == nil && s.Server {
				serverPrefix = "server_"
			}
			ircCmdNullable = sql.NullString{String: serverPrefix + strings.ToUpper(ircmsg.Command), Valid: true}
			reply := ircServer.ProcessMessage(msg.Id, msg.Session, ircmsg)
			ircServer.SendMessages(reply, msg.Session, sessionId.Id)
		}

	case types.RobustConfig:
		newCfg, err := config.FromString(string(msg.Data))
		if err != nil {
			log.Printf("Skipping unexpectedly invalid configuration (%v)\n", err)
		} else {
			netConfig = newCfg
			ircServer.Config = netConfig.IRC
		}
	}

	ircServer.CompactionDatabase.ExecStmt("_all", msg.Id.Id, sessionId.Id, ircCmdNullable)

	appliedMessages.WithLabelValues(msg.Type.String()).Inc()

	return nil
}

// Snapshot returns a list of pointers based on which a snapshot can be
// created. After restoring that snapshot, the server state (current sessions,
// channels, modes, …) should be identical to the state before taking the
// snapshot. Note that entries are compacted, i.e. useless state
// transformations (think multiple nickname changes) are skipped. Also note
// that the IRC output is not guaranteed to end up the same as before. This is
// not a problem in practice as only log entries which are older than a couple
// of days are compacted, and proxy connections are only disconnected for a
// couple of minutes at a time.
func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	first, err := fsm.ircstore.FirstIndex()
	if err != nil {
		return nil, err
	}

	last, err := fsm.ircstore.LastIndex()
	if err != nil {
		return nil, err
	}

	return &robustSnapshot{
		firstIndex: first,
		lastIndex:  last,
		store:      fsm.ircstore,
		del:        make(map[uint64]types.RobustId),
		servers:    make(map[int64]bool),
		idToIdx:    make(map[int64]uint64),
	}, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	log.Printf("Restoring snapshot\n")
	defer snap.Close()

	if err := fsm.ircstore.Close(); err != nil {
		log.Fatal(err)
	}
	// Deleting irclog and creating a new database is significantly faster than
	// using DeleteRange() on the entire keyspace. Re-creating the database
	// saves us 4 minutes of CPU time (out of 5 minutes total!) and >1G of
	// memory usage.
	irclogPath := filepath.Join(*raftDir, "irclog")
	if err := os.RemoveAll(irclogPath); err != nil {
		log.Fatal(err)
	}
	var err error
	ircStore, err = raft_store.NewLevelDBStore(irclogPath, true)
	if err != nil {
		log.Fatal(err)
	}
	fsm.ircstore = ircStore

	if err := ircServer.CompactionDatabase.Close(); err != nil {
		log.Fatal(err)
	}
	ircServer = ircserver.NewIRCServer(*raftDir, *network, time.Now())

	decoder := json.NewDecoder(snap)
	for {
		var entry raft.Log
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		fsm.Apply(&entry)
	}

	log.Printf("Restored snapshot\n")

	return nil
}
