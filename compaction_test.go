package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/robustirc/rafthttp"
	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/outputstream"
	"github.com/robustirc/robustirc/internal/raftstore"
	"github.com/robustirc/robustirc/internal/robust"
	"github.com/stapelberg/glog"

	"github.com/hashicorp/raft"
)

func appendLog(logs []*raft.Log, msg string) []*raft.Log {
	return append(logs, &raft.Log{
		Type:  raft.LogCommand,
		Index: uint64(len(logs) + 1),
		Data:  []byte(msg),
	})
}

func verifyEndState(t *testing.T) {
	s, err := ircServer.GetSession(robust.Id{Id: 1})
	if err != nil {
		t.Fatalf("No session found after applying log messages")
	}
	if s.Nick != "secure_" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure_")
	}

	// s.Channels is a map[lcChan]bool, so we copy it over.
	got := make(map[string]bool)
	for key, value := range s.Channels {
		got[string(key)] = value
	}

	want := make(map[string]bool)
	want["#chaos-hd"] = true

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session.Channels: got %v, want %v", got, want)
	}
}

func snapshot(fsm raft.FSM, fss raft.SnapshotStore, numLogs uint64) error {
	snapshot, err := fsm.Snapshot()
	if err != nil {
		return fmt.Errorf("Unexpected error in fsm.Snapshot(): %v", err)
	}

	robustsnap, ok := snapshot.(*robustSnapshot)
	if !ok {
		return fmt.Errorf("fsm.Snapshot() return value is not a robustSnapshot")
	}
	if robustsnap.lastIndex != numLogs {
		return fmt.Errorf("snapshot does not retain the last message, got: %d, want: %d", robustsnap.lastIndex, numLogs)
	}

	sink, err := fss.Create(
		1,                         // snapshot version
		numLogs,                   // index
		1,                         // term
		raft.Configuration{},      // configuration (peers)
		0,                         // configurationIndex
		&rafthttp.HTTPTransport{}) // only needed for EncodePeers, which is stateless
	if err != nil {
		return fmt.Errorf("fss.Create: %v", err)
	}

	if err := snapshot.Persist(sink); err != nil {
		return fmt.Errorf("Unexpected error in snapshot.Persist(): %v", err)
	}
	sink.Close()
	return nil
}

func restore(fsm raft.FSM, fss raft.SnapshotStore, numLogs uint64) error {
	snapshots, err := fss.List()
	if err != nil {
		return fmt.Errorf("fss.List(): %v", err)
	}
	// snapshots[0] is the most recent snapshot
	snapshotId := snapshots[0].ID
	_, readcloser, err := fss.Open(snapshotId)
	if err != nil {
		return fmt.Errorf("fss.Open(%s): %v", snapshotId, err)
	}

	if err := fsm.Restore(readcloser); err != nil {
		return fmt.Errorf("fsm.Restore(): %v", err)
	}

	first, _ := fsm.(*FSM).ircstore.FirstIndex()
	last, _ := fsm.(*FSM).ircstore.LastIndex()

	if last-first >= numLogs {
		return fmt.Errorf("Compaction did not decrease log size. got: %d, want: < %d", last-first, numLogs)
	}
	return nil
}

// TestCompaction does a full snapshot, persists it to disk, restores it and
// makes sure the state matches expectations. The other test functions directly
// test what should be compacted.
func TestCompaction(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())
	var err error
	outputStream, err = outputstream.NewOutputStream("")

	tempdir := t.TempDir()
	flag.Set("raftdir", tempdir)

	logstore, err := raftstore.NewLevelDBStore(filepath.Join(tempdir, "raftlog"), false, false)
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	ircstore, err := raftstore.NewLevelDBStore(filepath.Join(tempdir, "irclog"), false, false)
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	fsm := FSM{
		store:                logstore,
		ircstore:             ircstore,
		lastSnapshotState:    make(map[uint64][]byte),
		sessionExpirationDur: 10 * time.Minute,
	}

	var logs []*raft.Log
	logs = appendLog(logs, `{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`)
	logs = appendLog(logs, `{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`)
	logs = appendLog(logs, `{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`)
	logs = appendLog(logs, `{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure_"}`)
	logs = appendLog(logs, `{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`)
	logs = appendLog(logs, `{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #i3"}`)
	logs = appendLog(logs, `{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "PRIVMSG #chaos-hd :heya"}`)
	logs = appendLog(logs, `{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "PRIVMSG #chaos-hd :newer message"}`)
	logs = appendLog(logs, `{"Id": {"Id": 9}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #i3"}`)

	// These messages are too new to be compacted.
	nowID := time.Now().UnixNano()
	logs = appendLog(logs, `{"Id": {"Id": 10}, "UnixNano": `+strconv.FormatInt(nowID, 10)+`, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`)
	nowID++
	logs = appendLog(logs, `{"Id": {"Id": 11}, "UnixNano": `+strconv.FormatInt(nowID, 10)+`, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`)

	if err := logstore.StoreLogs(logs); err != nil {
		t.Fatalf("Unexpected error in store.StoreLogs: %v", err)
	}
	for _, log := range logs {
		fsm.Apply(log)
	}

	verifyEndState(t)

	fss, err := raft.NewFileSnapshotStore(tempdir, 5, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	// Snapshot twice so that we know state is carried over from one
	// snapshot to the next.
	if err := snapshot(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}
	// raft uses time.Now() in the snapshot name, so advance time by 1ms to
	// guarantee we get a different filename.
	time.Sleep(1 * time.Millisecond)
	if err := snapshot(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}

	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	if err := restore(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}

	verifyEndState(t)

	// Restore() a fresh FSM, then take another snapshot, restore it
	// and verify the end state. This covers the code path where the
	// previous snapshot was not done in the same process run.
	ircstore = fsm.ircstore
	fsm = FSM{
		store:             logstore,
		ircstore:          ircstore,
		lastSnapshotState: make(map[uint64][]byte),
	}

	if err := restore(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}

	if err := snapshot(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}

	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	if err := restore(&fsm, fss, uint64(len(logs))); err != nil {
		t.Fatal(err)
	}

	verifyEndState(t)
}

func TestMain(m *testing.M) {
	defer glog.Flush()
	flag.Parse()
	tempdir, err := ioutil.TempDir("", "robustirc-test-raftdir-")
	if err != nil {
		log.Fatal(err)
	}
	raftDir = &tempdir
	// TODO: cleanup tmp-outputstream and permanent-compaction*
	os.Exit(m.Run())
}
