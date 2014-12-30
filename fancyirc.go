package main

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

var (
	raftDir   = flag.String("raftdir", "/tmp/r", "")
	peers     = flag.String("peers", "", "comma-separated host:port tuples")
	listen    = flag.String("listen", ":8000", "")
	join      = flag.String("join", "", "raft master to join")
	node      *raft.Raft
	peerStore *raft.JSONPeers
	logStore  *fancyLogStore
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
		if s.lowIndex == 0 {
			s.lowIndex = entry.Index
		}
		if entry.Index > s.highIndex {
			s.highIndex = entry.Index
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

func handleJoin(res http.ResponseWriter, req *http.Request) {
	log.Println("Join request from", req.RemoteAddr)
	if node.State() != raft.Leader {
		log.Println("rejecting join request, I am not the leader")
		http.Redirect(res, req, fmt.Sprintf("http://%s/join", node.Leader()), 307)
		return
	}

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Println("Could not read body:", err)
		return
	}

	type joinRequest struct {
		Addr string
	}
	var r joinRequest

	if err = json.Unmarshal(data, &r); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(res, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Joining peer: %q\n", r.Addr)

	a, err := net.ResolveTCPAddr("tcp", r.Addr)
	if err != nil {
		log.Printf("Could not resolve addr %q: %v\n", r.Addr, err)
		http.Error(res, "Could not resolve your address", 400)
		return
	}

	if err = node.AddPeer(a).Error(); err != nil {
		log.Println("Could not add peer:", err)
		http.Error(res, "Could not add peer", 500)
		return
	}
	res.WriteHeader(200)
}

var statusTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
	<head>
		<title>Status of fancyirc node {{ .Addr }}</title>
	</head>
	<body>
		<h1>Status of fancyirc node {{ .Addr }}</h1>
		<dl>
			<dt>Status</dt>
			<dd>{{ .State }}</dd>
			<dt>Current Leader </dt>
			<dd>{{ .Leader }}</dd>
			<dt>Peers</dt>
			<dd>{{ .Peers }}</dd>
		</dl>
		<h2>Log</h2>
		<p>Entries {{ .First }} through {{ .Last }}</p>
		<ul>
		{{ range .Entries }}
			<li>{{ . }}</li>
		{{ end }}
		</ul>
		<h2>Stats</h2>
		<dl>
		{{ range $key, $val := .Stats }}
			<dt>{{ $key }}</dt>
			<dd>{{ $val }}</dt>
		{{ end }}
		</dl>
	</body>
</html>`))

func handleStatus(res http.ResponseWriter, req *http.Request) {
	p, _ := peerStore.Peers()

	lo, err := logStore.FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}
	hi, err := logStore.LastIndex()
	if err != nil {
		log.Printf("Could not get last index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}

	var entries []*raft.Log
	if lo != 0 && hi != 0 {
		for i := lo; i <= hi; i++ {
			l := new(raft.Log)

			if err := logStore.GetLog(i, l); err != nil {
				log.Printf("Could not get entry %d: %v", i, err)
				http.Error(res, "internal error", 500)
				return
			}
			entries = append(entries, l)
		}
	}

	args := struct {
		Addr    string
		State   raft.RaftState
		Leader  net.Addr
		Peers   []net.Addr
		First   uint64
		Last    uint64
		Entries []*raft.Log
		Stats   map[string]string
	}{
		*listen,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
	}

	statusTpl.Execute(res, args)
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

	if *join != "" {
		p = joinMaster(*join, peerStore)
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

	logStore = &fancyLogStore{}
	stablestore := &fancyStableStore{}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, logStore, stablestore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	//http.HandleFunc("/msg", handleMessages)
	http.HandleFunc("/", handleStatus)
	http.HandleFunc("/put", handleRequest)
	http.HandleFunc("/join", handleJoin)
	go func() {
		err := http.ListenAndServe(*listen, nil)
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
