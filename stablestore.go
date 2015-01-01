package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
)

type fancyStableStore struct {
}

func (s *fancyStableStore) Set(key []byte, val []byte) error {
	return ioutil.WriteFile(filepath.Join(*raftDir, "fancystable", string(key)), val, 0600)
}

func (s *fancyStableStore) Get(key []byte) ([]byte, error) {
	b, err := ioutil.ReadFile(filepath.Join(*raftDir, "fancystable", string(key)))
	if err != nil && os.IsNotExist(err) {
		return []byte{}, fmt.Errorf("not found")
	}
	return b, err
}

func (s *fancyStableStore) SetUint64(key []byte, val uint64) error {
	return s.Set(key, []byte(fmt.Sprintf("%d", val)))
}

func (s *fancyStableStore) GetUint64(key []byte) (uint64, error) {
	b, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	i, err := strconv.ParseInt(string(b), 0, 64)
	if err != nil {
		return 0, err
	}
	return uint64(i), nil
}
