package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	metrics "github.com/armon/go-metrics"
	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/config"
	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/outputstream"
	"github.com/robustirc/robustirc/internal/raftstore"
	"github.com/robustirc/robustirc/internal/robust"
	"github.com/stapelberg/glog"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/sorcix/irc.v2"

	pb "github.com/robustirc/robustirc/internal/proto"
)

type FSM struct {
	// Used for invalidating messages of death.
	store *raftstore.LevelDBStore

	ircstore *raftstore.LevelDBStore

	skipDeletionForCanary bool

	// lastSnapshotState is a map of the last included index to a
	// serialized pb.Snapshot (IRCServer state) which was taken the
	// last time a Raft snapshot was taken.
	lastSnapshotState map[uint64][]byte

	sessionExpirationMu  sync.RWMutex
	sessionExpirationDur time.Duration

	ReplaceState func(*ircserver.IRCServer, *raftstore.LevelDBStore, *outputstream.OutputStream)

	restoreMu sync.Mutex
}

func (fsm *FSM) sessionExpiration() time.Duration {
	fsm.sessionExpirationMu.RLock()
	defer fsm.sessionExpirationMu.RUnlock()
	return fsm.sessionExpirationDur
}

// sendMessages appends the specified batch of messages to the output,
// marking them as a response to the incoming message with id 'id' and
// associating them with session 'session'. IRC clients will
// eventually receive these messages by calling GetNext.
func sendMessages(reply *ircserver.Replyctx, session robust.Id, id uint64, o *outputstream.OutputStream) {
	if len(reply.Messages) == 0 || o == nil {
		return
	}

	converted := make([]outputstream.Message, len(reply.Messages))
	for idx, msg := range reply.Messages {
		converted[idx] = outputstream.Message{
			Id:             msg.Id,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		}
	}
	if err := o.Add(converted); err != nil {
		log.Panicf("Could not add messages to outputstream: %v\n", err)
	}
}

func (fsm *FSM) applyRobustMessage(msg *robust.Message, i *ircserver.IRCServer, o *outputstream.OutputStream) error {
	switch msg.Type {
	case robust.MessageOfDeath:
		// To prevent the message from being accepted again.
		i.UpdateLastClientMessageID(msg)
		log.Printf("Skipped message of death with msgid %d.\n", msg.Id.Id)

	case robust.CreateSession:
		return i.CreateSession(msg.Id, msg.Data, msg.Timestamp())
	case robust.DeleteSession:
		if _, err := i.GetSession(msg.Session); err == nil {
			// TODO(secure): overwrite QUIT messages for services with an faq entry explaining that they are not robust yet.
			reply := i.ProcessMessage(msg, irc.ParseMessage("QUIT :"+string(msg.Data)))
			i.SetLastProcessed(robust.Id{Id: msg.Id.Id})
			sendMessages(reply, msg.Session, msg.Id.Id, o)
			i.MaybeDeleteSession(msg.Session)
		}

	case robust.IRCFromClient:
		// Need to do this first, because ircserver.ProcessMessage could delete
		// the session, e.g. by using KILL or QUIT.
		if err := i.UpdateLastClientMessageID(msg); err != nil {
			log.Printf("Error updating the last message for session: %v\n", err)
		} else {
			ircmsg := irc.ParseMessage(msg.Data)
			reply := i.ProcessMessage(msg, ircmsg)
			i.SetLastProcessed(robust.Id{Id: msg.Session.Id})
			sendMessages(reply, msg.Session, msg.Session.Id, o)
			i.MaybeDeleteSession(msg.Session)
		}

	case robust.Config:
		newCfg, err := config.FromString(msg.Data)
		if err != nil {
			log.Printf("Skipping unexpectedly invalid configuration (%v)\n", err)
		} else {
			i.ConfigMu.Lock()
			defer i.ConfigMu.Unlock()
			i.Config = newCfg
			i.Config.Revision = msg.Revision
			fsm.sessionExpirationMu.Lock()
			defer fsm.sessionExpirationMu.Unlock()
			fsm.sessionExpirationDur = time.Duration(i.Config.SessionExpiration)
		}
	}
	return nil
}

func (fsm *FSM) applyProto(l *pb.RaftLog, msg *robust.Message) interface{} {
	defer func() {
		if msg.Type == robust.MessageOfDeath {
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
			msg.Type = robust.MessageOfDeath
			var (
				data []byte
				err  error
			)
			if l.Data[0] == 'p' {
				data, err = proto.Marshal(msg.ProtoMessage())
				if err != nil {
					glog.Fatalf("Could not marshal message: %v", err)
				}
				data = append([]byte{'p'}, data...)
			} else {
				// XXX(1.0): delete this branch, all messages use proto
				data, err = json.Marshal(msg)
				if err != nil {
					glog.Fatalf("Could not marshal message: %v", err)
				}
			}
			l.Data = data
			if err := fsm.store.StoreLogProto(l); err != nil {
				glog.Fatalf("Could not store log while marking message as message of death: %v", err)
			}
			log.Printf("Marked %+v as message of death\n", l)
			glog.Fatalf("%v", r)
		}
	}()

	err := fsm.applyRobustMessage(msg, ircServer, outputStream)

	appliedMessages.WithLabelValues(msg.Type.String()).Inc()

	return err
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	p := pb.RaftLog{
		Index:      l.Index,
		Term:       l.Term,
		Type:       pb.RaftLog_LogType(l.Type),
		Data:       l.Data,
		Extensions: l.Extensions,
		AppendedAt: timestamppb.New(l.AppendedAt),
	}

	if *useProtobuf {
		if err := fsm.ircstore.StoreLogProto(&p); err != nil {
			log.Panicf("Could not persist message in irclogs/: %v", err)
		}
	} else {
		if err := fsm.ircstore.StoreLog(l); err != nil {
			log.Panicf("Could not persist message in irclogs/: %v", err)
		}
	}

	msg := robust.NewMessageFromBytes(l.Data, robust.IdFromRaftIndex(l.Index))
	glog.Infof("Apply(msg.Type=%s)\n", msg.Type)
	return fsm.applyProto(&p, &msg)
}

// Snapshot returns a raftSnapshot, containing a snapshot of the
// IRCServer state and all messages which cannot be compacted yet
// because they are too new.  After restoring that snapshot, the
// server state (current sessions, channels, modes, …) should be
// identical to the state before taking the snapshot.
func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	start := time.Now()
	defer metrics.MeasureSince([]string{"robustirc", "fsm", "snapshot"}, start)

	first, err := fsm.ircstore.FirstIndex()
	if err != nil {
		return nil, err
	}

	last, err := fsm.ircstore.LastIndex()
	if err != nil {
		return nil, err
	}
	if first < 1 {
		return nil, fmt.Errorf("first index of ircstore (%d) is < 1", first)
	}

	log.Printf("Filtering and writing up to %d indexes (from %d to %d)\n", last-first+1, first, last)

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

	exp := fsm.sessionExpiration()
	if exp == 0 {
		// in case the config does not set SessionExpiration at all
		exp = 10 * time.Minute
	}
	// Keep messages for as long as they could possibly be useful to continue a
	// hanging session (think a user who suspends their notebook, walks around
	// for 9m, opens the notebook and wants to resume the same session in
	// RobustIRC).
	exp += expireSessionsInterval
	log.Printf("sessionExpiration is %v", exp)
	compactionEnd := compactionStart.Add(-1 * exp)

	tmpServer := ircserver.NewIRCServer("testnetwork", time.Now())
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
		if len(value) > 0 && value[0] == 'p' {
			var p pb.RaftLog
			if err := proto.Unmarshal(value[1:], &p); err != nil {
				glog.Errorf("Skipping log entry %d because of a proto unmarshaling error: %v", i, err)
				available = iterator.Next()
				continue
			}
			nlog.Index = p.Index
			nlog.Term = p.Term
			nlog.Type = raft.LogType(p.Type)
			nlog.Data = p.Data
			nlog.Extensions = p.Extensions
			nlog.AppendedAt = p.AppendedAt.AsTime()
		} else {
			// XXX(1.0): delete this branch, ircstore uses proto
			if err := json.Unmarshal(value, &nlog); err != nil {
				glog.Errorf("Skipping log entry %d because of a JSON unmarshaling error: %v", i, err)
				available = iterator.Next()
				continue
			}
		}
		available = iterator.Next()

		if nlog.Type != raft.LogCommand {
			return nil, fmt.Errorf("nlog.Type = %d instead of LogCommand", nlog.Type)
		}

		parsed := robust.NewMessageFromBytes(nlog.Data, robust.IdFromRaftIndex(nlog.Index))
		if parsed.Timestamp().After(compactionEnd) {
			first = i
			break
		}

		fsm.applyRobustMessage(&parsed, tmpServer, nil)

		if !fsm.skipDeletionForCanary {
			// TODO: make the following more efficient, we can whack out the entire range at once.
			if err := outputStream.Delete(parsed.Id); err != nil {
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
	start := time.Now()
	defer metrics.MeasureSince([]string{"robustirc", "fsm", "restore"}, start)

	log.Printf("Restoring snapshot\n")
	log.Printf("Acquiring restore lock")
	fsm.restoreMu.Lock()
	defer fsm.restoreMu.Unlock()
	log.Printf("Obtained restore lock")
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
	ircStore, err = raftstore.NewLevelDBStore(irclogPath, true, *useProtobuf)
	if err != nil {
		log.Fatal(err)
	}
	fsm.ircstore = ircStore
	if err := outputStream.Close(); err != nil {
		glog.Error(err)
	}

	ircServer = ircserver.NewIRCServer(*network, time.Now())
	outputStream, err = outputstream.NewOutputStream(*raftDir)
	if err != nil {
		log.Fatal(err)
	}
	fsm.ReplaceState(ircServer, ircStore, outputStream)
	// XXX(1.0): remove this conditional, all snapshots are protobuf-encoded now
	b := bufio.NewReader(snap)
	first, err := b.Peek(1)
	if err != nil {
		return err
	}
	if first[0] == 'p' {
		// protobuf snapshot prefix (invalid JSON)
		return fsm.decodeProtobuf(b)
	}
	if err := fsm.decodeJson(b); err != nil {
		return err
	}
	if *useProtobuf {
		return fsm.ircstore.ConvertToProto()
	}
	return nil
}

func (fsm *FSM) decodeProtobuf(b *bufio.Reader) error {
	start := time.Now()
	log.Printf("decoding protobuf snapshot")
	// discard leading 'p'
	if _, err := b.ReadByte(); err != nil {
		return err
	}
	var (
		lenbuf [8]byte // binary.Size(uint64(0))
		entry  pb.RaftLog
		batch  leveldb.Batch
	)
	for {
		if _, err := io.ReadFull(b, lenbuf[:]); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		buf := make([]byte, binary.BigEndian.Uint64(lenbuf[:]))
		if _, err := io.ReadFull(b, buf); err != nil {
			return err
		}
		if len(buf) > 0 {
			if got, want := buf[0], byte('p'); got != want {
				return fmt.Errorf("unexpected first byte: got %v, want %v", got, want)
			}
		}
		if err := proto.Unmarshal(buf[1:], &entry); err != nil {
			return err
		}
		msg := robust.NewMessageFromBytes(entry.Data, robust.IdFromRaftIndex(entry.Index))
		if msg.Type == robust.State {
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

		binary.BigEndian.PutUint64(lenbuf[:], entry.Index)
		batch.Put(lenbuf[:], buf)
		if batch.Len() > 100 {
			if err := fsm.ircstore.WriteBatch(&batch); err != nil {
				log.Panicf("Could not persist message in irclogs/: %v", err)
			}
			batch.Reset()
		}

		fsm.applyProto(&entry, &msg)
	}
	if err := fsm.ircstore.WriteBatch(&batch); err != nil {
		return err
	}

	log.Printf("Restored snapshot in %v", time.Since(start))
	return nil
}

func (fsm *FSM) decodeJson(b *bufio.Reader) error {
	start := time.Now()
	log.Printf("decoding JSON snapshot")
	decoder := json.NewDecoder(b)
	for {
		var entry raft.Log
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("Decode: %v", err)
		}

		msg := robust.NewMessageFromBytes(entry.Data, robust.IdFromRaftIndex(entry.Index))
		if msg.Type == robust.State {
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

	log.Printf("Restored snapshot in %v", time.Since(start))
	return nil
}
