package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/raftstore"
	"github.com/robustirc/robustirc/types"
)

type robustSnapshot struct {
	firstIndex    uint64
	lastIndex     uint64
	store         *raftstore.LevelDBStore
	state         []byte
	compactionEnd time.Time
}

// Persist writes a robustSnapshot to disk, i.e. handles the
// serialization details.
func (s *robustSnapshot) Persist(sink raft.SnapshotSink) error {
	stateMsg := types.RobustMessage{
		Type: types.RobustState,
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
	log.Printf("snapshot: wrote %d bytes", snapshotBytes)

	log.Printf("Snapshot done\n")

	return nil
}

func (s *robustSnapshot) Release() {
}
