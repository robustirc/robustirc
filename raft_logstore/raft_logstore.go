package raft_logstore

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/hashicorp/raft"
)

// trivial log store, writing one entry into one file each.
// fulfills the raft.LogStore interface.
type RobustLogStore struct {
	l         sync.RWMutex
	lowIndex  uint64
	highIndex uint64
	dir       string
}

func NewRobustLogStore(dir string) (*RobustLogStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "robustlogs"), 0700); err != nil {
		return nil, err
	}

	return &RobustLogStore{
		dir: dir,
	}, nil
}

func (s *RobustLogStore) FirstIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.lowIndex, nil
}

func (s *RobustLogStore) LastIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.highIndex, nil
}

func (s *RobustLogStore) GetLog(index uint64, rlog *raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()
	f, err := os.Open(filepath.Join(s.dir, fmt.Sprintf("robustlogs/entry.%d", index)))
	if err != nil {
		if os.IsNotExist(err) {
			return raft.ErrLogNotFound
		}
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(rlog)
}

func (s *RobustLogStore) StoreLog(log *raft.Log) error {
	return s.StoreLogs([]*raft.Log{log})
}

func (s *RobustLogStore) StoreLogs(logs []*raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()

	for _, entry := range logs {
		log.Printf("writing index %d to file\n", entry.Index)
		suffix := strconv.FormatUint(entry.Index, 10)
		f, err := os.Create(filepath.Join(s.dir, "robustlogs/incomplete."+suffix))
		if err != nil {
			return err
		}
		if err := json.NewEncoder(f).Encode(entry); err != nil {
			f.Close()
			return err
		}
		if entry.Index < s.lowIndex || s.lowIndex == 0 {
			s.lowIndex = entry.Index
		}
		if entry.Index > s.highIndex {
			s.highIndex = entry.Index
		}
		f.Close()
		if err := os.Rename(
			filepath.Join(s.dir, "robustlogs/incomplete."+suffix),
			filepath.Join(s.dir, "robustlogs/entry."+suffix)); err != nil {
			return err
		}
	}

	return nil
}

func (s *RobustLogStore) DeleteRange(min, max uint64) error {
	s.l.Lock()
	defer s.l.Unlock()
	for i := min; i <= max; i++ {
		log.Printf("deleting index %d\n", i)
		if err := os.Remove(filepath.Join(s.dir, fmt.Sprintf("robustlogs/entry.%d", i))); err != nil {
			return err
		}
	}
	s.lowIndex = max + 1
	return nil
}

func (s *RobustLogStore) DeleteAll() error {
	s.l.Lock()
	defer s.l.Unlock()
	if err := os.RemoveAll(filepath.Join(s.dir, "robustlogs")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "robustlogs"), 0700); err != nil {
		return err
	}
	s.lowIndex = 0
	s.highIndex = 0
	return nil
}
