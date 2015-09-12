package main

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/types"
	"github.com/robustirc/robustirc/util"
	"github.com/sorcix/irc"
)

type uint64Slice []uint64

func (p uint64Slice) Len() int           { return len(p) }
func (p uint64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p uint64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

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

	// Dump the in-memory state into a file, to be read by robustirc-canary.
	f, err := os.Create(statePath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	log.Printf("Dumping state for robustirc-canary into %q\n", statePath)

	enc := json.NewEncoder(f)

	// Sort the keys to iterate through |rs.parsed| in deterministic order.
	keys := make([]uint64, 0, len(rs.parsed))
	for idx := range rs.parsed {
		keys = append(keys, idx)
	}
	sort.Sort(uint64Slice(keys))

	for _, idx := range keys {
		nmsg := rs.parsed[idx]
		// TODO: come up with pseudo-values for createsession/deletesession
		if nmsg.Type != types.RobustIRCFromClient {
			continue
		}
		ircmsg := irc.ParseMessage(nmsg.Data)
		if ircmsg.Command == irc.PING || ircmsg.Command == irc.PONG {
			continue
		}
		vmsgs, _ := ircServer.Get(nmsg.Id)
		cm := canaryMessageState{
			Id:        idx,
			Session:   nmsg.Session.Id,
			Input:     util.PrivacyFilterIrcmsg(ircmsg).String(),
			Output:    make([]canaryMessageOutput, len(vmsgs)),
			Compacted: rs.del[idx],
		}
		for idx, vmsg := range vmsgs {
			ifc := make(map[string]bool)
			for k, v := range *vmsg.InterestingFor {
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
