package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"

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
	s, err := ircServer.GetSession(types.RobustId{Id: 1})
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

// TestCompaction does a full snapshot, persists it to disk, restores it and
// makes sure the state matches expectations. The other test functions directly
// test what should be compacted.
func TestCompaction(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	tempdir, err := ioutil.TempDir("", "robust-test-")
	if err != nil {
		t.Fatalf("ioutil.TempDir: %v", err)
	}
	defer os.RemoveAll(tempdir)

	logstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "raftlog"))
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	ircstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "irclog"))
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	fsm := FSM{logstore, ircstore}

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
	nowId := time.Now().UnixNano()
	logs = appendLog(logs, `{"Id": {"Id": `+strconv.FormatInt(nowId, 10)+`}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`)
	nowId += 1
	logs = appendLog(logs, `{"Id": {"Id": `+strconv.FormatInt(nowId, 10)+`}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`)

	if err := logstore.StoreLogs(logs); err != nil {
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
	if robustsnap.lastIndex != uint64(len(logs)) {
		t.Fatalf("snapshot does not retain the last message, got: %d, want: %d", robustsnap.lastIndex, len(logs))
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

	first, _ := ircstore.FirstIndex()
	last, _ := ircstore.LastIndex()

	if last-first >= uint64(len(logs)) {
		t.Fatalf("Compaction did not decrease log size. got: %d, want: < %d", last-first, len(logs))
	}

	verifyEndState(t)
}

type inMemorySink struct {
	b bytes.Buffer
}

func (s *inMemorySink) Write(p []byte) (n int, err error) {
	n, err = s.b.Write(p)
	return
}

func (s *inMemorySink) Close() error {
	return nil
}

func (s *inMemorySink) ID() string {
	return "inmemory"
}

func (s *inMemorySink) Cancel() error {
	return nil
}

func applyAndCompact(t *testing.T, input []string) []string {
	tempdir, err := ioutil.TempDir("", "robust-test-")
	if err != nil {
		t.Fatalf("ioutil.TempDir: %v", err)
	}
	defer os.RemoveAll(tempdir)

	logstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "raftlog"))
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	ircstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "irclog"))
	if err != nil {
		t.Fatalf("Unexpected error in NewLevelDBStore: %v", err)
	}
	fsm := FSM{logstore, ircstore}

	var logs []*raft.Log
	for _, msg := range input {
		logs = append(logs, &raft.Log{
			Type:  raft.LogCommand,
			Index: uint64(len(logs) + 1),
			Data:  []byte(msg),
		})
	}

	if err := logstore.StoreLogs(logs); err != nil {
		t.Fatalf("Unexpected error in store.StoreLogs: %v", err)
	}

	for _, log := range logs {
		fsm.Apply(log)
	}

	rawsnap, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Unexpected error in fsm.Snapshot: %v", err)
	}
	s := rawsnap.(*robustSnapshot)
	sink := inMemorySink{}
	s.Persist(&sink)

	dec := json.NewDecoder(&sink.b)
	var output []string
	for {
		var l raft.Log
		if err := dec.Decode(&l); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Unexpected error in json.Decode: %v", err)
		}
		output = append(output, string(l.Data))
	}

	return output
}

func mustMatchStrings(t *testing.T, input []string, got []string, want []string) {
	if reflect.DeepEqual(got, want) {
		return
	}

	t.Logf("input (%d messages):\n", len(input))
	for _, msg := range input {
		t.Logf("    %s\n", msg)
	}
	t.Logf("output (%d messages):\n", len(got))
	for _, msg := range got {
		t.Logf("    %s\n", msg)
	}
	t.Logf("expected (%d messages):\n", len(want))
	for _, msg := range want {
		t.Logf("    %s\n", msg)
	}
	t.Fatalf("compacted output does not match expectation: got %v, want %v\n", got, want)
}

func TestCompactNickNone(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure2"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :foo"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure3"}`,
	}

	output := applyAndCompact(t, input)
	// Nothing can be compacted: the first NICK is necessary so that the
	// session is loggedIn() for further messages, the second NICK is necessary
	// so that #chaos-hd’s topicNick has the correct value.
	mustMatchStrings(t, input, output, input)
}

func TestCompactNickOne(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure2"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :foo"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure3"}`,
		`{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :bar"}`,
	}
	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK secure3"}`,
		`{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :bar"}`,
	}

	output := applyAndCompact(t, input)
	// Only the first and third NICK remain.
	mustMatchStrings(t, input, output, want)
}

func TestJoinPart(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`,
	}

	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
	}

	output := applyAndCompact(t, input)
	mustMatchStrings(t, input, output, want)
}

func TestCompactTopic(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :foo"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :blah"}`,
	}

	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :blah"}`,
	}

	output := applyAndCompact(t, input)
	mustMatchStrings(t, input, output, want)
}

func TestJoinTopic(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "PRIVMSG #chaos-hd :blah"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :foo"}`,
		`{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`,
	}

	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		// The JOIN must be retained, otherwise TOPIC cannot succeed.
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC #chaos-hd :foo"}`,
		`{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`,
	}

	output := applyAndCompact(t, input)
	mustMatchStrings(t, input, output, want)
}

func TestCompactDoubleJoin(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "PART #chaos-hd"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
	}
	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #chaos-hd"}`,
	}

	output := applyAndCompact(t, input)
	// Only the last JOIN needs to remain.
	mustMatchStrings(t, input, output, want)
}

func TestCompactUser(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :bleh"}`,
	}
	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
	}

	output := applyAndCompact(t, input)
	mustMatchStrings(t, input, output, want)
}

func TestCompactInvalid(t *testing.T) {
	ircServer = ircserver.NewIRCServer("testnetwork", time.Now())

	input := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
		`{"Id": {"Id": 4}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK"}`,
		`{"Id": {"Id": 5}, "Session": {"Id": 1}, "Type": 2, "Data": "USER"}`,
		`{"Id": {"Id": 6}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN"}`,
		`{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "PART"}`,
		`{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "TOPIC"}`,
	}
	want := []string{
		`{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`,
		`{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`,
		`{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`,
	}

	output := applyAndCompact(t, input)
	mustMatchStrings(t, input, output, want)
}
