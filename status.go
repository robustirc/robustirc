package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/types"
	"github.com/robustirc/robustirc/util"

	pb "github.com/robustirc/robustirc/proto"
)

//go:generate go run gentmpl.go templates/header templates/footer templates/status templates/getmessage templates/sessions templates/state templates/statusirclog templates/irclog

func handleStatusGetMessage(w http.ResponseWriter, req *http.Request) {
	if err := Templates.ExecuteTemplate(w, "templates/getmessage", struct {
		Addr               string
		GetMessageRequests map[string]GetMessagesStats
		CurrentLink        string
		Sessions           map[types.RobustId]ircserver.Session
	}{
		Addr:               *peerAddr,
		GetMessageRequests: copyGetMessagesRequests(),
		CurrentLink:        "/status/getmessage",
		Sessions:           ircServer.GetSessions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleStatusSessions(w http.ResponseWriter, req *http.Request) {
	if err := Templates.ExecuteTemplate(w, "templates/sessions", struct {
		Addr               string
		Sessions           map[types.RobustId]ircserver.Session
		CurrentLink        string
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               *peerAddr,
		Sessions:           ircServer.GetSessions(),
		CurrentLink:        "/status/sessions",
		GetMessageRequests: copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleStatusState(w http.ResponseWriter, req *http.Request) {
	textState := "state serialization failed"
	state, err := ircServer.Marshal(0)
	if err != nil {
		textState = fmt.Sprintf("state serialization failed: %v", err)
	} else {
		var snapshot pb.Snapshot
		if err := proto.Unmarshal(state, &snapshot); err != nil {
			textState = fmt.Sprintf("unmarshaling state failed: %v", err)
		} else {
			snapshot = util.PrivacyFilterSnapshot(snapshot)
			var marshaler proto.TextMarshaler
			textState = marshaler.Text(&snapshot)
		}
	}

	if err := Templates.ExecuteTemplate(w, "templates/state", struct {
		Addr               string
		ServerState        string
		CurrentLink        string
		Sessions           map[types.RobustId]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               *peerAddr,
		ServerState:        textState,
		CurrentLink:        "/status/state",
		Sessions:           ircServer.GetSessions(),
		GetMessageRequests: copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleStatusIrclog(w http.ResponseWriter, req *http.Request) {
	lo, err := ircStore.FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	hi, err := ircStore.LastIndex()
	if err != nil {
		log.Printf("Could not get last index: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	// Show the last 50 messages by default.
	if hi > 50 && hi-50 > lo {
		lo = hi - 50
	}

	if offsetStr := req.FormValue("offset"); offsetStr != "" {
		offset, err := strconv.ParseInt(offsetStr, 0, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		lo = uint64(offset)
	}

	if hi > lo+50 {
		hi = lo + 50
	}

	var entries []*raft.Log
	if lo != 0 && hi != 0 {
		for i := lo; i <= hi; i++ {
			l := new(raft.Log)

			if err := ircStore.GetLog(i, l); err != nil {
				// Not every message goes into the ircStore (e.g. raft peer change
				// messages do not).
				continue
			}
			if l.Type == raft.LogCommand {
				msg := types.NewRobustMessageFromBytes(l.Data)
				msg.Data = msg.PrivacyFilter()
				l.Data, _ = json.Marshal(&msg)
			}
			entries = append(entries, l)
		}
	}

	prevOffset := int64(lo) - 50
	if prevOffset < 0 {
		prevOffset = 1
	}

	if err := Templates.ExecuteTemplate(w, "templates/statusirclog", struct {
		Addr               string
		First              uint64
		Last               uint64
		Entries            []*raft.Log
		PrevOffset         int64
		NextOffset         uint64
		CurrentLink        string
		Sessions           map[types.RobustId]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               *peerAddr,
		First:              lo,
		Last:               hi,
		Entries:            entries,
		PrevOffset:         prevOffset,
		NextOffset:         lo + 50,
		CurrentLink:        "/status/irclog",
		Sessions:           ircServer.GetSessions(),
		GetMessageRequests: copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleStatus(res http.ResponseWriter, req *http.Request) {
	p, _ := peerStore.Peers()

	// robustirc-rollingrestart wants a machine-readable version of the status.
	if req.Header.Get("Accept") == "application/json" {
		type jsonStatus struct {
			State          string
			Leader         string
			Peers          []string
			AppliedIndex   uint64
			CommitIndex    uint64
			LastContact    time.Time
			ExecutableHash string
			CurrentTime    time.Time
		}
		res.Header().Set("Content-Type", "application/json")
		leaderStr := node.Leader()
		stats := node.Stats()
		appliedIndex, err := strconv.ParseUint(stats["applied_index"], 0, 64)
		if err != nil {
			http.Error(res, err.Error(), http.StatusInternalServerError)
			return
		}
		commitIndex, err := strconv.ParseUint(stats["commit_index"], 0, 64)
		if err != nil {
			http.Error(res, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(res).Encode(jsonStatus{
			State:          node.State().String(),
			Leader:         leaderStr,
			AppliedIndex:   appliedIndex,
			CommitIndex:    commitIndex,
			Peers:          p,
			LastContact:    node.LastContact(),
			ExecutableHash: executablehash,
			CurrentTime:    time.Now(),
		}); err != nil {
			log.Printf("%v\n", err)
			http.Error(res, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	args := struct {
		Addr               string
		State              raft.RaftState
		Leader             string
		Peers              []string
		Stats              map[string]string
		Sessions           map[types.RobustId]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
		NetConfig          config.Network
		CurrentLink        string
	}{
		Addr:               *peerAddr,
		State:              node.State(),
		Leader:             node.Leader(),
		Peers:              p,
		Stats:              node.Stats(),
		Sessions:           ircServer.GetSessions(),
		GetMessageRequests: copyGetMessagesRequests(),
		NetConfig:          ircServer.Config,
		CurrentLink:        "/status",
	}

	if err := Templates.ExecuteTemplate(res, "templates/status", args); err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleIrclog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("sessionid"), 0, 64)
	if err != nil || id == 0 {
		http.Error(w, "Invalid session", http.StatusBadRequest)
		return
	}

	session := types.RobustId{Id: id}

	// TODO(secure): pagination

	var messages []*types.RobustMessage
	first, _ := ircStore.FirstIndex()
	last, _ := ircStore.LastIndex()
	iterator := ircStore.GetBulkIterator(first, last+1)
	defer iterator.Release()
	for iterator.Next() {
		var elog raft.Log
		if err := json.Unmarshal(iterator.Value(), &elog); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if elog.Type != raft.LogCommand {
			continue
		}
		msg := types.NewRobustMessageFromBytes(elog.Data)
		if msg.Session.Id == session.Id {
			messages = append(messages, &msg)
		}
		output, ok := ircServer.Get(msg.Id)
		if ok {
			for _, msg := range output {
				if !msg.InterestingFor[session.Id] {
					continue
				}
				messages = append(messages, msg)
			}
		}
	}

	if err := iterator.Error(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	args := struct {
		Session  types.RobustId
		Messages []*types.RobustMessage
	}{
		session,
		messages,
	}
	if err := Templates.ExecuteTemplate(w, "templates/irclog", args); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
