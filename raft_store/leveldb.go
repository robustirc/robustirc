package raft_store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hashicorp/raft"
	"github.com/syndtr/goleveldb/leveldb"
)

var metaKey = []byte("logstore-meta")

type LevelDBStore struct {
	mu   sync.RWMutex
	meta logstoreMeta
	db   *leveldb.DB
}

type logstoreMeta struct {
	Lo uint64
	Hi uint64
}

// NewLevelDBStore opens a leveldb at the given directory to be used as a log-
// and stable storage for raft.
func NewLevelDBStore(dir string) (*LevelDBStore, error) {
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		if _, ok := err.(leveldb.ErrCorrupted); !ok {
			return nil, fmt.Errorf("could not open: %v", err)
		}
		db, err = leveldb.RecoverFile(dir, nil)
		if err != nil {
			return nil, fmt.Errorf("could not recover: %v", err)
		}
	}

	v, err := db.Get(metaKey, nil)
	if err != nil {
		if err != leveldb.ErrNotFound {
			db.Close()
			return nil, fmt.Errorf("error reading metadata: %v", err)
		}
		v = []byte(`{"Lo":0,"Hi":0}`)
	}
	var m logstoreMeta
	if err = json.Unmarshal(v, &m); err != nil {
		db.Close()
		return nil, err
	}

	return &LevelDBStore{db: db, meta: m}, nil
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

	return s.meta.Lo, nil
}

// LastIndex implements raft.LogStore.
func (s *LevelDBStore) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.meta.Hi, nil
}

// GetLog implements raft.LogStore.
func (s *LevelDBStore) GetLog(index uint64, rlog *raft.Log) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := make([]byte, binary.Size(index))
	binary.LittleEndian.PutUint64(key, index)
	value, err := s.db.Get(key, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(value, rlog)
}

// StoreLog implements raft.LogStore.
func (s *LevelDBStore) StoreLog(entry *raft.Log) error {
	return s.StoreLogs([]*raft.Log{entry})
}

// StoreLogs implements raft.LogStore.
func (s *LevelDBStore) StoreLogs(logs []*raft.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, binary.Size(uint64(0)))
	meta := s.meta

	for _, entry := range logs {
		binary.LittleEndian.PutUint64(key, entry.Index)
		v, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		batch.Put(key, v)

		if entry.Index < meta.Lo || meta.Lo == 0 {
			meta.Lo = entry.Index
		}
		if entry.Index > meta.Hi {
			meta.Hi = entry.Index
		}
	}
	buf := new(bytes.Buffer)
	json.NewEncoder(buf).Encode(meta)
	batch.Put(metaKey, buf.Bytes())
	if err := s.db.Write(&batch, nil); err != nil {
		return err
	}
	s.meta = meta
	return nil
}

// DeleteRange implements raft.LogStore.
func (s *LevelDBStore) DeleteRange(min, max uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, binary.Size(uint64(0)))
	meta := s.meta

	if min > meta.Lo && max < meta.Hi {
		panic("wrongly assumed that the range of stored keys is always contiguous")
	}

	for n := min; n <= max; n++ {
		binary.LittleEndian.PutUint64(key, n)
		batch.Delete(key)
	}
	if max == meta.Hi && min == meta.Lo {
		meta.Lo = 0
		meta.Hi = 0
	} else if max < meta.Hi {
		// We are deleting from the beginning. Update meta.Lo
		meta.Lo = max + 1
	} else if min > meta.Lo {
		// We are deleting from the end. Update meta.Hi
		meta.Hi = min - 1
	}

	buf := new(bytes.Buffer)
	json.NewEncoder(buf).Encode(meta)
	batch.Put(metaKey, buf.Bytes())

	if err := s.db.Write(&batch, nil); err != nil {
		return err
	}
	s.meta = meta
	return nil
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
	binary.LittleEndian.PutUint64(v, val)

	return s.db.Put(key, v, nil)
}

// GetUint64 implements raft.StableStore.
func (s *LevelDBStore) GetUint64(key []byte) (uint64, error) {
	key = append([]byte("stablestore-"), key...)
	v, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(v), err
}
