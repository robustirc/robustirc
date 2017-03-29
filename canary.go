package main

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/types"
	"github.com/robustirc/robustirc/util"
	"github.com/stapelberg/glog"
	"gopkg.in/sorcix/irc.v2"
)

type canaryMessageOutput struct {
	Text           string
	InterestingFor map[string]bool
}

type canaryMessageState struct {
	Id        uint64
	Session   int64
	Input     string
	Output    []canaryMessageOutput
	Compacted bool
}

func messagesString(messages []*types.RobustMessage) string {
	output := make([]string, len(messages))

	for idx, msg := range messages {
		output[idx] = "→ " + msg.Data
	}

	return strings.Join(output, "\n")
}

func canary(fsm raft.FSM, statePath string) {
	// Create a snapshot (only creates metadata) and persist it (does the
	// actual compaction). Afterwards we have access to |rs.parsed| (all
	// raft log entries, but parsed) and |rs.del| (all messages which were
	// just compacted).
	log.Printf("Compacting before dumping state\n")

	fsm.(*FSM).skipDeletionForCanary = true

	snapshot, err := fsm.Snapshot()
	if err != nil {
		log.Fatalf("fsm.Snapshot(): %v\n", err)
	}

	rs, ok := snapshot.(*robustSnapshot)
	if !ok {
		log.Fatalf("snapshot is not a robustSnapshot")
	}

	sink, err := raft.NewDiscardSnapshotStore().Create(rs.lastIndex, 1, []byte{})
	if err != nil {
		log.Fatalf("DiscardSnapshotStore.Create(): %v\n", err)
	}

	if err := snapshot.Persist(sink); err != nil {
		log.Fatalf("snapshot.Persist(): %v\n", err)
	}

	sink.Close()

	// Dump the in-memory state into a file, to be read by robustirc-canary.
	f, err := os.Create(statePath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	log.Printf("Dumping state for robustirc-canary into %q\n", statePath)

	enc := json.NewEncoder(f)

	iterator := rs.store.GetBulkIterator(rs.firstIndex, rs.lastIndex+1)
	defer iterator.Release()
	available := iterator.First()
	for available {
		var nlog raft.Log
		if err := iterator.Error(); err != nil {
			glog.Errorf("Error while iterating through the log: %v", err)
			available = iterator.Next()
			continue
		}
		idx := binary.BigEndian.Uint64(iterator.Key())
		value := iterator.Value()
		if err := json.Unmarshal(value, &nlog); err != nil {
			glog.Errorf("Skipping log entry %d because of a JSON unmarshaling error: %v", idx, err)
			continue
		}
		available = iterator.Next()

		// TODO: compact raft messages as well, so that peer changes are not kept forever
		if nlog.Type != raft.LogCommand {
			continue
		}

		nmsg := types.NewRobustMessageFromBytes(nlog.Data)
		if time.Unix(0, nmsg.Id.Id).Before(rs.compactionEnd) {
			continue
		}

		// TODO: come up with pseudo-values for createsession/deletesession
		if nmsg.Type != types.RobustIRCFromClient {
			continue
		}
		ircmsg := irc.ParseMessage(nmsg.Data)
		if ircmsg.Command == irc.PING || ircmsg.Command == irc.PONG {
			continue
		}
		vmsgs, _ := outputStream.Get(nmsg.Id)
		cm := canaryMessageState{
			Id:        idx,
			Session:   nmsg.Session.Id,
			Input:     util.PrivacyFilterIrcmsg(ircmsg).String(),
			Output:    make([]canaryMessageOutput, len(vmsgs)),
			Compacted: false,
		}
		for idx, vmsg := range vmsgs {
			ifc := make(map[string]bool)
			for k, v := range vmsg.InterestingFor {
				ifc["0x"+strconv.FormatInt(k, 16)] = v
			}
			cm.Output[idx] = canaryMessageOutput{
				Text:           util.PrivacyFilterIrcmsg(irc.ParseMessage(vmsg.Data)).String(),
				InterestingFor: ifc,
			}
		}
		if err := enc.Encode(&cm); err != nil {
			log.Fatal(err)
		}
	}
}
