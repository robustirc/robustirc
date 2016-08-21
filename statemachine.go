package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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

	skipDeletionForCanary bool

	// lastSnapshotState is a map of the last included index to a
	// serialized pb.Snapshot (IRCServer state) which was taken the
	// last time a Raft snapshot was taken.
	lastSnapshotState map[uint64][]byte
}

func applyRobustMessage(msg *types.RobustMessage, i *ircserver.IRCServer) {
	switch msg.Type {
	case types.RobustMessageOfDeath:
		// To prevent the message from being accepted again.
		i.UpdateLastClientMessageID(msg)
		log.Printf("Skipped message of death with msgid %d.\n", msg.Id.Id)

	case types.RobustCreateSession:
		i.CreateSession(msg.Id, msg.Data)
	case types.RobustDeleteSession:
		if _, err := i.GetSession(msg.Session); err == nil {
			// TODO(secure): overwrite QUIT messages for services with an faq entry explaining that they are not robust yet.
			reply := i.ProcessMessage(msg.Id, msg.Session, irc.ParseMessage("QUIT :"+string(msg.Data)))
			i.SendMessages(reply, msg.Session, msg.Id.Id)
		}

	case types.RobustIRCFromClient:
		// Need to do this first, because ircserver.ProcessMessage could delete
		// the session, e.g. by using KILL or QUIT.
		if err := i.UpdateLastClientMessageID(msg); err != nil {
			log.Printf("Error updating the last message for session: %v\n", err)
		} else {
			ircmsg := irc.ParseMessage(msg.Data)
			reply := i.ProcessMessage(msg.Id, msg.Session, ircmsg)
			i.SendMessages(reply, msg.Session, msg.Session.Id)
		}

	case types.RobustConfig:
		newCfg, err := config.FromString(msg.Data)
		if err != nil {
			log.Printf("Skipping unexpectedly invalid configuration (%v)\n", err)
		} else {
			i.Config = newCfg
			i.Config.Revision = msg.Revision
		}
	}
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

	applyRobustMessage(&msg, ircServer)

	appliedMessages.WithLabelValues(msg.Type.String()).Inc()

	return nil
}

// Snapshot returns a raftSnapshot, containing a snapshot of the
// IRCServer state and all messages which cannot be compacted yet
// because they are too new.  After restoring that snapshot, the
// server state (current sessions, channels, modes, …) should be
// identical to the state before taking the snapshot.
func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	first, err := fsm.ircstore.FirstIndex()
	if err != nil {
		return nil, err
	}

	last, err := fsm.ircstore.LastIndex()
	if err != nil {
		return nil, err
	}
	if first < 1 {
		return nil, fmt.Errorf("first index of ircstore is < 1")
	}

	log.Printf("Filtering and writing up to %d indexes (from %d to %d)\n", last-first, first, last)

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

	compactionEnd := compactionStart.Add(-7 * 24 * time.Hour)

	tmpServer := ircserver.NewIRCServer("", "testnetwork", time.Now())
	defer tmpServer.Close()
	if oldState, ok := fsm.lastSnapshotState[first-1]; !ok {
		if first == 1 {
			// This is the first snapshot which this RobustIRC network
			// is taking, there cannot be previous state.
		} else {
			// XXX(1.0): Reword the message once compatibility is broken.
			glog.Errorf("No snapshot state containing index %d found. Unless you just upgraded this node from v0.3, this is a BUG.", first-1)
		}
	} else {
		if _, err := tmpServer.Unmarshal(oldState); err != nil {
			return nil, err
		}
		// All snapshot states but first-1 can now be deleted. first-1
		// needs to be retained in case the snapshot which is
		// currently in progress fails and needs to be repeated.
		for key, _ := range fsm.lastSnapshotState {
			if key == first-1 {
				continue
			}
			delete(fsm.lastSnapshotState, key)
		}
	}

	iterator := fsm.ircstore.GetBulkIterator(first, last+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		var nlog raft.Log
		if err := iterator.Error(); err != nil {
			return nil, err
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
			return nil, fmt.Errorf("nlog.Type = %d instead of LogCommand", nlog.Type)
		}

		parsed := types.NewRobustMessageFromBytes(nlog.Data)
		if time.Unix(0, parsed.Id.Id).After(compactionEnd) {
			first = i
			break
		}

		applyRobustMessage(&parsed, tmpServer)

		if !fsm.skipDeletionForCanary {
			// TODO: make the following more efficient, we can whack out the entire range at once.
			if err := ircServer.Delete(parsed.Id); err != nil {
				log.Panicf("Could not delete outputstream message: %v\n", err)
			}
			fsm.ircstore.DeleteRange(i, i)
		}
	}

	state, err := tmpServer.Marshal(first - 1)
	if err != nil {
		return nil, err
	}

	fsm.lastSnapshotState[first-1] = state

	return &robustSnapshot{
		firstIndex:    first,
		lastIndex:     last,
		state:         state,
		store:         fsm.ircstore,
		compactionEnd: compactionEnd,
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
	if err := ircServer.Close(); err != nil {
		glog.Error(err)
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

		msg := types.NewRobustMessageFromBytes(entry.Data)
		if msg.Type == types.RobustState {
			log.Printf("found RobustState, unmarshalling\n")
			state, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				return err
			}
			lastIncludedIndex, err := ircServer.Unmarshal(state)
			if err != nil {
				return err
			}
			log.Printf("storing RobustState as index %d\n", lastIncludedIndex)
			fsm.lastSnapshotState[lastIncludedIndex] = state
			continue
		}

		fsm.Apply(&entry)
	}

	log.Printf("Restored snapshot\n")

	return nil
}
