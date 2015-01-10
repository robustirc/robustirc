package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
)

type robustStableStore struct {
	dir string
}

func NewRobustStableStore(dir string) (*robustStableStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "robuststable"), 0700); err != nil {
		return nil, err
	}

	return &robustStableStore{
		dir: dir,
	}, nil
}

func (s *robustStableStore) Set(key []byte, val []byte) error {
	return ioutil.WriteFile(filepath.Join(s.dir, "robuststable", string(key)), val, 0600)
}

func (s *robustStableStore) Get(key []byte) ([]byte, error) {
	b, err := ioutil.ReadFile(filepath.Join(s.dir, "robuststable", string(key)))
	if err != nil && os.IsNotExist(err) {
		return []byte{}, fmt.Errorf("not found")
	}
	return b, err
}

func (s *robustStableStore) SetUint64(key []byte, val uint64) error {
	return s.Set(key, []byte(fmt.Sprintf("%d", val)))
}

func (s *robustStableStore) GetUint64(key []byte) (uint64, error) {
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
