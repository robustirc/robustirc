package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/raftstore"
	"github.com/robustirc/robustirc/internal/robust"

	pb "github.com/robustirc/robustirc/internal/proto"
)

type robustSnapshot struct {
	firstIndex    uint64
	lastIndex     uint64
	store         *raftstore.LevelDBStore
	state         []byte
	compactionEnd time.Time
}

func writeLenPrefixed(sink raft.SnapshotSink, val []byte) (n int, err error) {
	var lenbuf [8]byte // binary.Size(uint64(0))
	binary.BigEndian.PutUint64(lenbuf[:], uint64(len(val)))
	nLen, err := sink.Write(lenbuf[:])
	if err != nil {
		return nLen, err
	}
	nVal, err := sink.Write(val)
	if err != nil {
		return nVal, err
	}
	return nLen + nVal, nil
}

func (s *robustSnapshot) persistJSON(sink raft.SnapshotSink) error {
	start := time.Now()
	log.Printf("persisting JSON-encoded snapshot")
	stateMsg := robust.Message{
		Type: robust.State,
		Data: base64.StdEncoding.EncodeToString(s.state), // TODO: find a more straight-forward way to encode this
	}
	stateMsgJson, err := json.Marshal(&stateMsg)
	if err != nil {
		return err
	}
	stateMsgRaftJson, err := json.Marshal(&raft.Log{
		Type:  raft.LogCommand,
		Index: 0, // never passed to raft.
		Data:  stateMsgJson,
	})
	if err != nil {
		return err
	}

	log.Printf("Copying non-deleted messages into snapshot\n")

	snapshotBytes, err := sink.Write(stateMsgRaftJson)
	if err != nil {
		return err
	}
	iterator := s.store.GetBulkIterator(s.firstIndex, s.lastIndex+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		if err := iterator.Error(); err != nil {
			return err
		}
		n, err := sink.Write(iterator.Value())
		if err != nil {
			return err
		}
		snapshotBytes += n

		available = iterator.Next()
	}
	log.Printf("snapshot: wrote %d bytes in %v", snapshotBytes, time.Since(start))

	log.Printf("Snapshot done\n")

	return nil
}

// Persist writes a robustSnapshot to disk, i.e. handles the
// serialization details.
func (s *robustSnapshot) Persist(sink raft.SnapshotSink) error {
	if !*useProtobuf {
		// XXX(1.0): delete this branch
		return s.persistJSON(sink)
	}
	start := time.Now()
	log.Printf("persisting protobuf-encoded snapshot")
	var snapshotBytes int
	n, err := sink.Write([]byte{'p'}) // signal a protobuf snapshot
	if err != nil {
		return err
	}
	snapshotBytes += n

	stateMsg := robust.Message{
		Type: robust.State,
		Data: base64.StdEncoding.EncodeToString(s.state), // TODO: find a more straight-forward way to encode this
	}

	stateMsgBytes, err := proto.Marshal(stateMsg.ProtoMessage())
	if err != nil {
		return err
	}

	stateMsgProto, err := proto.Marshal(&pb.RaftLog{
		Type:  pb.RaftLog_COMMAND,
		Index: 0, // never passed to raft
		Data:  append([]byte{'p'}, stateMsgBytes...),
	})
	if err != nil {
		return err
	}

	log.Printf("Copying non-deleted messages into snapshot\n")

	n, err = writeLenPrefixed(sink, append([]byte{'p'}, stateMsgProto...))
	if err != nil {
		return err
	}
	snapshotBytes += n
	iterator := s.store.GetBulkIterator(s.firstIndex, s.lastIndex+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		if err := iterator.Error(); err != nil {
			return err
		}
		n, err := writeLenPrefixed(sink, iterator.Value())
		if err != nil {
			return err
		}
		snapshotBytes += n

		available = iterator.Next()
	}
	log.Printf("snapshot: wrote %d bytes in %v", snapshotBytes, time.Since(start))

	log.Printf("Snapshot done\n")

	return nil
}

func (s *robustSnapshot) Release() {
}
