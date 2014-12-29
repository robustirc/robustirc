package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/hashicorp/raft-mdb"
)

var (
	raftDir = flag.String("raftdir", "/tmp/r", "")
	peers   = flag.String("peers", "", "comma-separated host:port tuples")
	listen  = flag.String("listen", ":6000", "")
)

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

	mdb, err := raftmdb.NewMDBStore(*raftDir)
	if err != nil {
		log.Fatal(err)
	}

	storage, err := NewStorage()
	if err != nil {
		log.Fatal(err)
	}

	fsm := &FSM{storage}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err := raft.NewRaft(config, fsm, mdb, mdb, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

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
