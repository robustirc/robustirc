// Package raftstore implements a storage backend for raft on top of LevelDB.
//
// LevelDBStore implements the LogStore and StableStore interfaces of
// https://godoc.org/github.com/hashicorp/raft by using
// https://godoc.org/github.com/syndtr/goleveldb as a storage backend.
package raftstore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/raftlog"
	"github.com/robustirc/robustirc/internal/robust"
	"github.com/syndtr/goleveldb/leveldb"
	leveldb_errors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/robustirc/robustirc/internal/proto"
)

// LevelDBStore implements the raft.LogStore and raft.StableStore interfaces on
// top of leveldb.
type LevelDBStore struct {
	mu sync.RWMutex
	db *leveldb.DB

	// XXX(1.0): delete these fields
	useProtobuf bool
	dir         string
}

// NewLevelDBStore opens a leveldb at the given directory to be used as a log-
// and stable storage for raft.
func NewLevelDBStore(dir string, errorIfExist bool, useProtobuf bool) (*LevelDBStore, error) {
	db, err := leveldb.OpenFile(dir, &opt.Options{ErrorIfExist: errorIfExist})
	if err != nil {
		if errorIfExist && err == os.ErrExist {
			// TODO: migrate this check to raft.HasExistingState
			return nil, fmt.Errorf("You specified -singlenode or -join, but %q already contains data, indicating this node is already part of a RobustIRC network. THIS IS UNSAFE! It will lead to split-brain scenarios and data-loss. Please see http://robustirc.net/docs/adminguide.html#_healing_partitions if you are trying to heal a network partition.", dir)
		}
		if _, ok := err.(*leveldb_errors.ErrCorrupted); !ok {
			return nil, fmt.Errorf("could not open: %v", err)
		}
		db, err = leveldb.RecoverFile(dir, nil)
		if err != nil {
			return nil, fmt.Errorf("could not recover: %v", err)
		}
	}

	s := &LevelDBStore{db: db, useProtobuf: useProtobuf, dir: dir}
	if useProtobuf {
		return s, s.ConvertToProto()
	}
	return s, nil
}

// convertToProto converts the database to use protobuf-encoded values instead
// of json-encoded values. This is a no-op once the database has been converted.
func (s *LevelDBStore) ConvertToProto() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	log.Printf("converting database %q to proto if necessary", s.dir)
	start := time.Now()
	defer func() { log.Printf("database conversion done in %v", time.Since(start)) }()
	i := s.db.NewIterator(nil, nil)
	defer i.Release()
	if !i.First() {
		return nil
	}
	for bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
		if !i.Next() {
			return nil
		}
	}

	var (
		batch leveldb.Batch
		rlog  pb.RaftLog
	)
	msg := &pb.RobustMessage{
		Id:      &pb.RobustId{},
		Session: &pb.RobustId{},
	}
	for !bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
		val := i.Value()
		l, err := raftlog.FromBytes(val)
		if err != nil {
			return err
		}
		if l.Type != raft.LogCommand {
			if len(val) > 0 && val[0] != 'p' {
				rlog.Data = l.Data
				rlog.Index = l.Index
				rlog.Term = l.Term
				rlog.Type = pb.RaftLog_LogType(l.Type)
				rlog.Extensions = l.Extensions
				rlog.AppendedAt = timestamppb.New(l.AppendedAt)
				v, err := proto.Marshal(&rlog)
				if err != nil {
					return err
				}
				batch.Put(i.Key(), append([]byte{'p'}, v...))
			}

			if !i.Next() {
				break
			}
			continue
		}
		if (len(val) > 0 && val[0] != 'p') ||
			(len(l.Data) > 0 && l.Data[0] != 'p') {
			m := robust.NewMessageFromBytes(l.Data, robust.IdFromRaftIndex(l.Index))
			m.CopyToProtoMessage(msg)
			v, err := proto.Marshal(msg)
			if err != nil {
				return err
			}
			rlog.Data = append([]byte{'p'}, v...)
			rlog.Index = l.Index
			rlog.Term = l.Term
			rlog.Type = pb.RaftLog_LogType(l.Type)
			rlog.Extensions = l.Extensions
			rlog.AppendedAt = timestamppb.New(l.AppendedAt)
			v, err = proto.Marshal(&rlog)
			if err != nil {
				return err
			}
			batch.Put(i.Key(), append([]byte{'p'}, v...))
		} else {
			// database already converted
			log.Printf("database already converted")
			return nil
		}
		if batch.Len() > 100 {
			if err := s.db.Write(&batch, nil); err != nil {
				return err
			}
			batch.Reset()
		}

		if !i.Next() {
			break
		}
	}
	return s.db.Write(&batch, nil)
}

// Close closes the LevelDBStore. No other methods may be called after this.
func (s *LevelDBStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Close()
	s.db = nil
	return err
}

// FirstIndex implements raft.LogStore.
func (s *LevelDBStore) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	i := s.db.NewIterator(nil, nil)
	defer i.Release()
	if !i.First() {
		return 0, nil
	}
	for bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
		if !i.Next() {
			return 0, nil
		}
	}
	return binary.BigEndian.Uint64(i.Key()), nil
}

// LastIndex implements raft.LogStore.
func (s *LevelDBStore) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	i := s.db.NewIterator(nil, nil)
	defer i.Release()
	if !i.Last() {
		return 0, nil
	}
	for bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
		if !i.Prev() {
			return 0, nil
		}
	}
	return binary.BigEndian.Uint64(i.Key()), nil
}

// GetBulkIterator returns an iterator which can be used to read the database
// entries in [start, limit). It performs much better than looping over GetLog.
func (s *LevelDBStore) GetBulkIterator(start, limit uint64) iterator.Iterator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	startKey := make([]byte, binary.Size(start))
	limitKey := make([]byte, binary.Size(limit))
	binary.BigEndian.PutUint64(startKey, start)
	binary.BigEndian.PutUint64(limitKey, limit)
	return s.db.NewIterator(&util.Range{
		Start: startKey,
		Limit: limitKey,
	}, &opt.ReadOptions{
		// This function is for reading through (almost) the entire database in
		// bulk, so caching the blocks does not make sense.
		DontFillCache: true,
	})
}

// GetLog implements raft.LogStore.
func (s *LevelDBStore) GetLog(index uint64, rlog *raft.Log) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := make([]byte, binary.Size(index))
	binary.BigEndian.PutUint64(key, index)
	value, err := s.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return raft.ErrLogNotFound
		}
		return err
	}
	if len(value) > 0 && value[0] == 'p' {
		var msg pb.RaftLog
		if err := proto.Unmarshal(value[1:], &msg); err != nil {
			return err
		}
		rlog.Index = msg.Index
		rlog.Term = msg.Term
		rlog.Type = raft.LogType(msg.Type)
		rlog.Data = msg.Data
		rlog.Extensions = msg.Extensions
		rlog.AppendedAt = msg.AppendedAt.AsTime()
		return nil
	} else {
		// XXX(1.0): delete this branch, all stores use proto
		return json.Unmarshal(value, rlog)
	}
}

// StoreLog implements raft.LogStore.
func (s *LevelDBStore) StoreLog(entry *raft.Log) error {
	return s.StoreLogs([]*raft.Log{entry})
}

func (s *LevelDBStore) WriteBatch(batch *leveldb.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Write(batch, nil)
}

// StoreLogs implements raft.LogStore.
func (s *LevelDBStore) StoreLogs(logs []*raft.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, binary.Size(uint64(0)))

	if s.useProtobuf {
		var msg pb.RaftLog
		for _, entry := range logs {
			binary.BigEndian.PutUint64(key, entry.Index)
			msg.Index = entry.Index
			msg.Term = entry.Term
			msg.Type = pb.RaftLog_LogType(entry.Type)
			msg.Data = entry.Data
			msg.Extensions = entry.Extensions
			msg.AppendedAt = timestamppb.New(entry.AppendedAt)
			v, err := proto.Marshal(&msg)
			if err != nil {
				return err
			}
			batch.Put(key, append([]byte{'p'}, v...))
		}
	} else {
		for _, entry := range logs {
			binary.BigEndian.PutUint64(key, entry.Index)
			v, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			batch.Put(key, v)
		}
	}

	return s.db.Write(&batch, nil)
}

func (s *LevelDBStore) StoreLogProto(msg *pb.RaftLog) error {
	v, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	var batch leveldb.Batch
	key := make([]byte, binary.Size(uint64(0)))
	binary.BigEndian.PutUint64(key, msg.Index)
	batch.Put(key, append([]byte{'p'}, v...))

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Write(&batch, nil)
}

// DeleteRange implements raft.LogStore.
func (s *LevelDBStore) DeleteRange(min, max uint64) error {
	iterator := s.GetBulkIterator(min, max+1)
	defer iterator.Release()

	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	available := iterator.First()
	for available {
		if err := iterator.Error(); err != nil {
			return err
		}
		batch.Delete(iterator.Key())
		available = iterator.Next()
	}
	return s.db.Write(&batch, nil)
}

// Set implements raft.StableStore.
func (s *LevelDBStore) Set(key []byte, val []byte) error {
	key = append([]byte("stablestore-"), key...)
	return s.db.Put(key, val, nil)
}

// Get implements raft.StableStore.
func (s *LevelDBStore) Get(key []byte) ([]byte, error) {
	key = append([]byte("stablestore-"), key...)
	value, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	return value, err
}

// SetUint64 implements raft.StableStore.
func (s *LevelDBStore) SetUint64(key []byte, val uint64) error {
	key = append([]byte("stablestore-"), key...)

	v := make([]byte, binary.Size(val))
	binary.BigEndian.PutUint64(v, val)

	return s.db.Put(key, v, nil)
}

// GetUint64 implements raft.StableStore.
func (s *LevelDBStore) GetUint64(key []byte) (uint64, error) {
	key = append([]byte("stablestore-"), key...)
	v, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	return binary.BigEndian.Uint64(v), err
}
