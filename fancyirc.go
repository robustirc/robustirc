package main

import (
	"encoding/binary"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

var (
	raftDir    = flag.String("raftdir", "/tmp/r", "")
	peers      = flag.String("peers", "", "comma-separated host:port tuples")
	listen     = flag.String("listen", ":6000", "")
	listenhttp = flag.String("listenhttp", ":8000", "")
	node       *raft.Raft
)

// trivial log store, writing one entry into one file each.
// fulfills the raft.LogStore interface.
type fancyLogStore struct {
	l         sync.RWMutex
	lowIndex  uint64
	highIndex uint64
}

func (s *fancyLogStore) FirstIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.lowIndex, nil
}

func (s *fancyLogStore) LastIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.highIndex, nil
}

func (s *fancyLogStore) GetLog(index uint64, rlog *raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()
	f, err := os.Open(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", index)))
	if err != nil {
		return err
	}
	defer f.Close()

	var elog raft.Log
	if err := gob.NewDecoder(f).Decode(&elog); err != nil {
		return err
	}
	*rlog = elog
	return nil
}

func (s *fancyLogStore) StoreLog(log *raft.Log) error {
	return s.StoreLogs([]*raft.Log{log})
}

func (s *fancyLogStore) StoreLogs(logs []*raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()

	for _, entry := range logs {
		log.Printf("writing index %d to file (%v)\n", entry.Index, entry)
		f, err := os.Create(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", entry.Index)))
		if err != nil {
			return err
		}
		defer f.Close()
		if err := gob.NewEncoder(f).Encode(entry); err != nil {
			return err
		}
	}

	return nil
}

func (s *fancyLogStore) DeleteRange(min, max uint64) error {
	s.l.Lock()
	defer s.l.Unlock()
	for i := min; i <= max; i++ {
		log.Printf("deleting index %d\n", i)
		if err := os.Remove(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", i))); err != nil {
			return err
		}
	}
	return nil
}

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

// Snapshot holds the data needed to serialize storage
type Snapshot struct {
	uuids   [][]byte
	entries []string
}

func uint32ToBytes(u uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, u)
	return buf
}

// Converts bytes to an integer
func bytesToUint32(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}

// Persist writes a snapshot to a file. We just serialize all active entries.
func (s *Snapshot) Persist(sink raft.SnapshotSink) error {
	_, err := sink.Write([]byte{0x0})
	if err != nil {
		sink.Cancel()
		return err
	}

	for i, e := range s.entries {
		_, err = sink.Write(s.uuids[i])
		if err != nil {
			sink.Cancel()
			return err
		}

		b := []byte(e)
		_, err = sink.Write(uint32ToBytes(uint32(len(b))))
		if err != nil {
			sink.Cancel()
			return err
		}

		_, err = sink.Write(b)
		if err != nil {
			sink.Cancel()
			return err
		}
	}

	return sink.Close()
}

// Release cleans up a snapshot. We don't need to do anything.
func (s *Snapshot) Release() {
}

type FSM struct {
	store *Storage
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	log.Printf("TODO: apply %v\n", l)
	return nil
}

func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	uuids, entries, err := fsm.store.GetAll()
	snapshot := &Snapshot{uuids, entries}
	return snapshot, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	defer snap.Close()

	s, err := NewStorage()
	if err != nil {
		return err
	}

	// swap in the restored storage, with time emission channels.
	s.c = fsm.store.c
	s.C = fsm.store.C
	fsm.store.Close()
	fsm.store = s

	b := make([]byte, 1)
	_, err = snap.Read(b)
	if b[0] != byte(0x00) {
		msg := "Unknown snapshot schema version"
		log.Printf(msg)
		return fmt.Errorf(msg)
	}

	uuid := make([]byte, 16)
	size := make([]byte, 4)
	count := 0
	for {
		_, err = snap.Read(uuid)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		_, err = snap.Read(size)
		if err != nil {
			return err
		}

		eb := make([]byte, bytesToUint32(size))
		_, err = snap.Read(eb)
		if err != nil {
			return err
		}
		e := string(eb)
		err = s.Add(uuid, e)
		if err != nil {
			return err
		}

		count++
	}

	log.Printf("Restored snapshot. entries=%d", count)
	return nil
}

func handleRequest(res http.ResponseWriter, req *http.Request) {
	log.Printf("reading body…\n")
	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Println("error reading request:", err)
		return
	}
	log.Printf("proposing()\n")
	node.Apply(data, 10*time.Second)
	log.Println("proposed")
}

func main() {
	flag.Parse()
	log.Printf("hey\n")

	a, err := net.ResolveTCPAddr("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	transport, err := raft.NewTCPTransport(*listen, a, 3, 10*time.Second, nil)
	if err != nil {
		log.Fatal(err)
	}

	peerStore := raft.NewJSONPeers(*raftDir, transport)

	var p []net.Addr

	config := raft.DefaultConfig()
	if *peers == "" {
		config.EnableSingleNode = true
	} else {
		p, err = peerStore.Peers()
		if err != nil {
			log.Fatal(err)
		}
		for _, addr := range strings.Split(*peers, ",") {
			peer, err := net.ResolveTCPAddr("tcp", addr)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("adding %v\n", peer)
			p = raft.AddUniquePeer(p, peer)
			peerStore.SetPeers(p)
		}
	}

	fss, err := raft.NewFileSnapshotStore(*raftDir, 1, nil)
	if err != nil {
		log.Fatal(err)
	}

	storage, err := NewStorage()
	if err != nil {
		log.Fatal(err)
	}

	fsm := &FSM{storage}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancylogs"), 0755); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancystable"), 0755); err != nil {
		log.Fatal(err)
	}

	ls := &fancyLogStore{}
	stablestore := &fancyStableStore{}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, ls, stablestore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	//http.HandleFunc("/msg", handleMessages)
	http.HandleFunc("/put", handleRequest)
	go func() {
		err := http.ListenAndServe(*listenhttp, nil)
		if err != nil {
			log.Fatal(err)
		}
	}()

	node.SetPeers(p)

	for {

		time.Sleep(1000 * time.Millisecond)

		// err = s.raft.Add(b, raftMaxTime)
		// if err != nil {
		// 	eWrapper.Done(false)
		// }

		// eWrapper.Done(true)

	}
}
