package raft_logstore

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

type uint64Slice []uint64

func (p uint64Slice) Len() int           { return len(p) }
func (p uint64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p uint64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func NewRobustLogStore(dir string) (*RobustLogStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "robustlogs"), 0700); err != nil {
		return nil, err
	}

	return &RobustLogStore{
		dir: dir,
	}, nil
}

// GetAll returns all indexes that are currently present in the log store. This
// is NOT part of the raft.LogStore interface — we use it when snapshotting.
func (s *RobustLogStore) GetAll() ([]uint64, error) {
	var indexes []uint64
	dir, err := os.Open(filepath.Join(s.dir, "robustlogs"))
	if err != nil {
		return indexes, err
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return indexes, err
	}

	for _, name := range names {
		if !strings.HasPrefix(name, "entry.") {
			continue
		}

		dot := strings.LastIndex(name, ".")
		if dot == -1 {
			continue
		}

		index, err := strconv.ParseInt(name[dot+1:], 0, 64)
		if err != nil {
			return indexes, fmt.Errorf("Unexpected filename, does not confirm to entry.%%d: %q. Parse error: %v", name, err)
		}

		indexes = append(indexes, uint64(index))
	}

	sort.Sort(uint64Slice(indexes))

	return indexes, nil
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

	var elog raft.Log
	if err := json.NewDecoder(f).Decode(&elog); err != nil {
		return err
	}
	*rlog = elog
	return nil
}

func (s *RobustLogStore) StoreLog(log *raft.Log) error {
	return s.StoreLogs([]*raft.Log{log})
}

func (s *RobustLogStore) StoreLogs(logs []*raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()

	for _, entry := range logs {
		log.Printf("writing index %d to file (%v)\n", entry.Index, entry)
		f, err := os.Create(filepath.Join(s.dir, fmt.Sprintf("robustlogs/entry.%d", entry.Index)))
		if err != nil {
			return err
		}
		defer f.Close()
		if err := json.NewEncoder(f).Encode(entry); err != nil {
			return err
		}
		if entry.Index < s.lowIndex || s.lowIndex == 0 {
			s.lowIndex = entry.Index
		}
		if entry.Index > s.highIndex {
			s.highIndex = entry.Index
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
