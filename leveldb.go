package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/raft"
	"github.com/syndtr/goleveldb/leveldb"
)

var metaKey = []byte("logstore-meta")

type LevelDB struct {
	mu   sync.RWMutex
	meta logstoreMeta
	db   *leveldb.DB
}

type logstoreMeta struct {
	Lo uint64
	Hi uint64
}

func NewLevelDB(dir string) (*LevelDB, error) {
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
			return nil, fmt.Errorf("no meta value stored: %v", err)
		}
		v = make([]byte, 16)
	}
	var m logstoreMeta
	if err = binary.Read(bytes.NewReader(v), binary.LittleEndian, &m); err != nil {
		db.Close()
		return nil, err
	}

	return &LevelDB{db: db, meta: m}, nil
}

func (s *LevelDB) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Close()
	s.db = nil
	return err
}

func (s *LevelDB) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.meta.Lo, nil
}

func (s *LevelDB) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.meta.Hi, nil
}

func (s *LevelDB) GetLog(index uint64, rlog *raft.Log) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, index)
	value, err := s.db.Get(key, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(value, rlog)
}

func (s *LevelDB) StoreLog(entry *raft.Log) error {
	return s.StoreLogs([]*raft.Log{entry})
}

func (s *LevelDB) StoreLogs(logs []*raft.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, 8)
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
	binary.Write(buf, binary.LittleEndian, meta)
	batch.Put(metaKey, buf.Bytes())
	if err := s.db.Write(&batch, nil); err != nil {
		return err
	}
	s.meta = meta
	return nil
}

func (s *LevelDB) DeleteRange(min, max uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, 8)
	meta := s.meta

	if min > meta.Lo && max < meta.Hi {
		panic("wrongly assumed that the range of stored keys is always contiguous")
	}

	for n := min; n <= max; n++ {
		binary.LittleEndian.PutUint64(key, n)
		batch.Delete(key)
	}
	if max < meta.Hi {
		meta.Lo = max + 1
	}
	if min > meta.Lo {
		meta.Hi = min - 1
	}

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, meta)
	batch.Put(metaKey, buf.Bytes())

	if err := s.db.Write(&batch, nil); err != nil {
		return err
	}
	s.meta = meta
	return nil
}

func (s *LevelDB) DeleteAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var batch leveldb.Batch
	key := make([]byte, 8)

	for n := s.meta.Lo; n <= s.meta.Hi; n++ {
		binary.LittleEndian.PutUint64(key, n)
		batch.Delete(key)
	}
	meta := logstoreMeta{0, 0}

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, meta)
	batch.Put(metaKey, buf.Bytes())

	if err := s.db.Write(&batch, nil); err != nil {
		return err
	}
	s.meta = meta
	return nil
}

func (s *LevelDB) Set(key []byte, val []byte) error {
	key = append([]byte("stablestore-"), key...)
	return s.db.Put(key, val, nil)
}

func (s *LevelDB) Get(key []byte) ([]byte, error) {
	key = append([]byte("stablestore-"), key...)
	value, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, errors.New("not found")
	}
	return value, err
}

func (s *LevelDB) SetUint64(key []byte, val uint64) error {
	key = append([]byte("stablestore-"), key...)

	v := make([]byte, 8)
	binary.LittleEndian.PutUint64(v, val)

	return s.db.Put(key, v, nil)
}

func (s *LevelDB) GetUint64(key []byte) (uint64, error) {
	key = append([]byte("stablestore-"), key...)
	v, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return 0, errors.New("not found")
	}
	return binary.LittleEndian.Uint64(v), err
}
