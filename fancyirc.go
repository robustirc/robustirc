package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"fancyirc/types"

	"github.com/gorilla/mux"
	"github.com/hashicorp/raft"
	"github.com/sorcix/irc"
)

var (
	raftDir    = flag.String("raftdir", "/tmp/r", "")
	singleNode = flag.Bool("singlenode", false, "set to true iff starting the first node for the first time")
	listen     = flag.String("listen", ":8000", "")
	join       = flag.String("join", "", "raft master to join")
	network    = flag.String("network", "fancy.twice-irc.de", "Name of the network. Ideally also a DNS name pointing to one or more servers.")

	node      *raft.Raft
	peerStore *raft.JSONPeers
	logStore  *fancyLogStore
)

type fancySnapshot struct {
	indexes []uint64
	store   *fancyLogStore
}

func (s *fancySnapshot) Persist(sink raft.SnapshotSink) error {
	log.Printf("Persisting indexes %v\n", s.indexes)
	encoder := gob.NewEncoder(sink)
	var entry raft.Log
	for _, index := range s.indexes {
		if err := s.store.GetLog(index, &entry); err != nil {
			sink.Cancel()
			return err
		}
		if err := encoder.Encode(entry); err != nil {
			sink.Cancel()
			return err
		}
	}
	sink.Close()
	return nil
}

func (s *fancySnapshot) Release() {
}

type FSM struct {
	store *fancyLogStore
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	msg := types.NewFancyMessageFromBytes(l.Data)
	log.Printf("Apply(fmsg.Type=%d)\n", msg.Type)

	switch msg.Type {
	case types.FancyCreateSession:
		sessions[msg.Id] = &Session{
			Id:       msg.Id,
			StartIdx: CurrentIdx(),
			Channels: make(map[string]bool),
		}
		log.Printf("sessions now: %+v\n", sessions)

	case types.FancyDeleteSession:
		processMessage(msg.Session, msg.Id.Id, irc.ParseMessage("QUIT :"+string(msg.Data)))
		delete(sessions, msg.Session)

	case types.FancyIRCFromClient:
		processMessage(msg.Session, msg.Id.Id, irc.ParseMessage(string(msg.Data)))
	}

	return nil
}

func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	indexes, err := fsm.store.GetAll()
	return &fancySnapshot{indexes, fsm.store}, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	log.Printf("Restoring snapshot\n")
	defer snap.Close()

	if err := os.RemoveAll(filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	if err := os.Rename(filepath.Join(*raftDir, "fancylogs"),
		filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancylogs"), 0755); err != nil {
		return err
	}

	decoder := gob.NewDecoder(snap)
	for {
		var entry raft.Log
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// TODO(secure): is it okay to re-apply these entries? i.e., when
		// restoring snapshots during normal operation (when does that
		// happen?), will we re-send messages to clients?
		fsm.Apply(&entry)

		if err := fsm.store.StoreLog(&entry); err != nil {
			return err
		}
	}

	if err := os.RemoveAll(filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	log.Printf("Restored snapshot\n")

	return nil
}

func joinMaster(addr string, peerStore *raft.JSONPeers) []net.Addr {
	master, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		log.Fatalf("Could not resolve %q: %v", addr, err)
	}

	type joinRequest struct {
		Addr string
	}
	var buf *bytes.Buffer
	if data, err := json.Marshal(joinRequest{*listen}); err != nil {
		log.Fatal("Could not marshal join request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}
	if res, err := http.Post(fmt.Sprintf("http://%s/join", addr), "application/json", buf); err != nil {
		log.Fatal("Could not send join request:", err)
	} else if res.StatusCode > 399 {
		data, _ := ioutil.ReadAll(res.Body)
		log.Fatal("Join request failed:", string(data))
	} else if res.StatusCode > 299 {
		loc := res.Header.Get("Location")
		if loc == "" {
			log.Fatal("Redirect has no Location header")
		}
		u, err := url.Parse(loc)
		if err != nil {
			log.Fatalf("Could not parse redirection %q: %v", loc, err)
		}

		return joinMaster(u.Host, peerStore)
	}

	log.Printf("Adding master %v as peer\n", master)
	p, err := peerStore.Peers()
	if err != nil {
		log.Fatal("Could not read peers:", err)
	}
	p = raft.AddUniquePeer(p, master)
	peerStore.SetPeers(p)
	return p
}

func main() {
	flag.Parse()
	log.Printf("hey\n")

	a, err := net.ResolveTCPAddr("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	transport := NewTransport(a)
	http.Handle("/raft/", transport)

	peerStore = raft.NewJSONPeers(*raftDir, transport)

	var p []net.Addr

	config := raft.DefaultConfig()
	if *singleNode {
		config.EnableSingleNode = true
	}

	// Keep 5 snapshots in *raftDir/snapshots, log to stderr.
	fss, err := raft.NewFileSnapshotStore(*raftDir, 5, nil)
	if err != nil {
		log.Fatal(err)
	}

	// TODO(secure): remove this, it’s only for forcing many snapshots right now.
	config.SnapshotThreshold = 2
	config.SnapshotInterval = 1 * time.Second

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancylogs"), 0755); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancystable"), 0755); err != nil {
		log.Fatal(err)
	}

	logStore = &fancyLogStore{}
	stablestore := &fancyStableStore{}
	fsm := &FSM{logStore}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, logStore, stablestore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	r := mux.NewRouter()
	r.HandleFunc("/", handleStatus)
	r.HandleFunc("/join", handleJoin)
	r.HandleFunc("/snapshot", handleSnapshot)
	r.HandleFunc("/fancyirc/v1/session", handleCreateSession).Methods("POST")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}", handleDeleteSession).Methods("DELETE")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/message", handlePostMessage).Methods("POST")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/messages", handleGetMessages).Methods("GET")
	http.Handle("/", r)

	go func() {
		err := http.ListenAndServe(*listen, nil)
		if err != nil {
			log.Fatal(err)
		}
	}()

	if *join != "" {
		p = joinMaster(*join, peerStore)
	}

	if len(p) > 0 {
		node.SetPeers(p)
	}

	for {
		time.Sleep(30 * time.Second)
		peers, err := peerStore.Peers()
		if err != nil {
			log.Fatalf("Could not get peers: %v (Peer file corrupted on disk?)\n", err)
		}
		SendPing(node.Leader(), peers)
	}
}
