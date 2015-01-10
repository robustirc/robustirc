package main

import (
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_logstore"
	"github.com/robustirc/robustirc/types"

	"github.com/hashicorp/raft"
	"github.com/sorcix/irc"
)

func appendLog(logs []*raft.Log, msg string) []*raft.Log {
	return append(logs, &raft.Log{
		Type:  raft.LogCommand,
		Index: uint64(len(logs)),
		Data:  []byte(msg),
	})
}

func verifyEndState(t *testing.T) {
	s, ok := ircserver.GetSession(types.RobustId{Id: 1})
	if !ok {
		t.Fatalf("No session found after applying log messages")
	}
	if s.Nick != "secure_" {
		t.Fatalf("session.Nick: got %q, want %q", s.Nick, "secure_")
	}

	want := make(map[string]bool)
	want["#chaos-hd"] = true

	if !reflect.DeepEqual(s.Channels, want) {
		t.Fatalf("session.Channels: got %v, want %v", s.Channels, want)
	}
}

func TestCompaction(t *testing.T) {
	ircserver.ClearState()
	ircserver.ServerPrefix = &irc.Prefix{Name: "testnetwork"}

	tempdir, err := ioutil.TempDir("", "robust-test-")
	if err != nil {
		t.Fatalf("ioutil.TempDir: %v", err)
	}
	defer os.RemoveAll(tempdir)

	store, err := raft_logstore.NewRobustLogStore(tempdir)
	if err != nil {
		t.Fatalf("Unexpected error in NewRobustLogStore: %v", err)
	}
	fsm := FSM{store}

	var logs []*raft.Log
	logs = appendLog(logs, `{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`)
	logs = appendLog(logs, `{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`)
	logs = appendLog(logs, `{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure_"}`)
	logs = appendLog(logs, `{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`)
	logs = appendLog(logs, `{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #i3"}`)
	logs = appendLog(logs, `{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "PRIVMSG #chaos-hd :heya"}`)
	logs = appendLog(logs, `{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "PRIVMSG #chaos-hd :newer message"}`)
	logs = appendLog(logs, `{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #i3"}`)

	// These messages are too new to be compacted.
	nowId := time.Now().UnixNano()
	logs = appendLog(logs, `{"Id": {"Id": `+strconv.FormatInt(nowId, 10)+`}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`)
	nowId += 1
	logs = appendLog(logs, `{"Id": {"Id": `+strconv.FormatInt(nowId, 10)+`}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`)

	if err := store.StoreLogs(logs); err != nil {
		t.Fatalf("Unexpected error in store.StoreLogs: %v", err)
	}
	for _, log := range logs {
		fsm.Apply(log)
	}

	verifyEndState(t)

	snapshot, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Unexpected error in fsm.Snapshot(): %v", err)
	}

	robustsnap, ok := snapshot.(*robustSnapshot)
	if !ok {
		t.Fatalf("fsm.Snapshot() return value is not a robustSnapshot")
	}
	if robustsnap.indexes[len(robustsnap.indexes)-1] != uint64(len(logs)-1) ||
		robustsnap.indexes[len(robustsnap.indexes)-2] != uint64(len(logs)-2) {
		t.Fatalf("snapshot does not retain the last two (recent) messages")
	}

	fss, err := raft.NewFileSnapshotStore(tempdir, 5, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	sink, err := fss.Create(uint64(len(logs)), 1, []byte{})
	if err != nil {
		t.Fatalf("fss.Create: %v", err)
	}

	if err := snapshot.Persist(sink); err != nil {
		t.Fatalf("Unexpected error in snapshot.Persist(): %v", err)
	}

	snapshots, err := fss.List()
	if err != nil {
		t.Fatalf("fss.List(): %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots): got %d, want 1", len(snapshots))
	}
	_, readcloser, err := fss.Open(snapshots[0].ID)
	if err != nil {
		t.Fatalf("fss.Open(%s): %v", snapshots[0].ID, err)
	}

	if err := fsm.Restore(readcloser); err != nil {
		t.Fatalf("fsm.Restore(): %v", err)
	}

	indexes, err := store.GetAll()
	if err != nil {
		t.Fatalf("store.GetAll(): %v", err)
	}

	if len(indexes) >= len(logs) {
		t.Fatalf("Compaction did not decrease log size. got: %d, want: < %d", len(indexes), len(logs))
	}

	verifyEndState(t)
}
