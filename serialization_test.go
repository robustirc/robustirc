package main

import (
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"
)

func createIrcServer(tempdir string) (*raft_store.LevelDBStore, *raft_store.LevelDBStore, FSM, error) {
	ircServer = ircserver.NewIRCServer("", "testnetwork", time.Now())
	flag.Set("raftdir", tempdir)

	logstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "raftlog"), false)
	if err != nil {
		return nil, nil, FSM{}, err
	}
	ircstore, err := raft_store.NewLevelDBStore(filepath.Join(tempdir, "irclog"), false)
	if err != nil {
		return nil, nil, FSM{}, err
	}
	fsm := FSM{store: logstore, ircstore: ircstore}
	return logstore, ircstore, fsm, nil
}

func TestSerialization(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "robust-test-")
	if err != nil {
		t.Fatalf("ioutil.TempDir: %v", err)
	}
	defer os.RemoveAll(tempdir)

	logstore, _, fsm, err := createIrcServer(tempdir)
	if err != nil {
		t.Fatal(err)
	}

	var logs []*raft.Log
	logs = appendLog(logs, `{"Id": {"Id": 1}, "Type": 0, "Data": "auth"}`)
	logs = appendLog(logs, `{"Id": {"Id": 2}, "Session": {"Id": 1}, "Type": 2, "Data": "NICK sECuRE"}`)
	logs = appendLog(logs, `{"Id": {"Id": 3}, "Session": {"Id": 1}, "Type": 2, "Data": "USER blah 0 * :Michael Stapelberg"}`)
	logs = appendLog(logs, `{"Id": {"Id": 4}, "Type": 0, "Data": "auth"}`)
	logs = appendLog(logs, `{"Id": {"Id": 5}, "Session": {"Id": 4}, "Type": 2, "Data": "NICK mero"}`)
	logs = appendLog(logs, `{"Id": {"Id": 6}, "Session": {"Id": 4}, "Type": 2, "Data": "USER blah 0 * :Axel Wagner"}`)
	logs = appendLog(logs, `{"Id": {"Id": 7}, "Session": {"Id": 1}, "Type": 2, "Data": "JOIN #test"}`)
	logs = appendLog(logs, `{"Id": {"Id": 8}, "Session": {"Id": 1}, "Type": 2, "Data": "MODE #test +i"}`)
	logs = appendLog(logs, `{"Id": {"Id": 9}, "Session": {"Id": 1}, "Type": 2, "Data": "INVITE mero #test"}`)

	if err := logstore.StoreLogs(logs); err != nil {
		t.Fatalf("Unexpected error in store.StoreLogs: %v", err)
	}
	for _, log := range logs {
		fsm.Apply(log)
	}

	state, err := ircServer.Marshal(uint64(len(logs)))
	if err != nil {
		t.Fatalf("Could not serialize state: %v", err)
	}

	tempdir, err = ioutil.TempDir("", "robust-test-")
	if err != nil {
		t.Fatalf("ioutil.TempDir: %v", err)
	}
	defer os.RemoveAll(tempdir)

	logstore, _, fsm, err = createIrcServer(tempdir)
	if err != nil {
		t.Fatal(err)
	}

	lastIncludedIndex, err := ircServer.Unmarshal(state)
	if err != nil {
		t.Fatalf("Could not deserialize state: %v", err)
	}
	if got, want := lastIncludedIndex, uint64(len(logs)); got != want {
		t.Fatalf("lastIncludedIndex is not equal to what we wrote, got %d, want %d", got, want)
	}

	logs = nil
	logs = appendLog(logs, `{"Id": {"Id": 10}, "Session": {"Id": 4}, "Type": 2, "Data": "JOIN #test"}`)
	if err := logstore.StoreLogs(logs); err != nil {
		t.Fatalf("Unexpected error in store.StoreLogs: %v", err)
	}
	for _, log := range logs {
		fsm.Apply(log)
	}

	msg, ok := ircServer.Get(types.RobustId{Id: 10})
	if !ok {
		t.Fatalf("JOIN message did not result in any output")
	}
	joinFound := false
	for _, msg := range msg {
		if msg.Type != types.RobustIRCToClient {
			continue
		}
		ircmsg := irc.ParseMessage(string(msg.Data))
		if ircmsg.Command != irc.JOIN {
			continue
		}

		joinFound = true
		break
	}
	if !joinFound {
		t.Fatalf("No JOIN message found")
	}
}
