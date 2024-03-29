package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/config"
	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/privacy"
	"github.com/robustirc/robustirc/internal/raftlog"
	"github.com/robustirc/robustirc/internal/robust"

	pb "github.com/robustirc/robustirc/internal/proto"
)

//go:generate go run gentmpl.go -package=api templates/header templates/footer templates/status templates/getmessage templates/sessions templates/state templates/statusirclog templates/irclog

func (api *HTTP) handleStatusGetMessage(w http.ResponseWriter, req *http.Request) {
	if err := templates.ExecuteTemplate(w, "templates/getmessage", struct {
		Addr               string
		GetMessageRequests map[string]GetMessagesStats
		CurrentLink        string
		Sessions           map[robust.Id]ircserver.Session
	}{
		Addr:               api.peerAddr,
		GetMessageRequests: api.copyGetMessagesRequests(),
		CurrentLink:        "/status/getmessage",
		Sessions:           api.ircServer().GetSessions(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (api *HTTP) handleStatusSessions(w http.ResponseWriter, req *http.Request) {
	if err := templates.ExecuteTemplate(w, "templates/sessions", struct {
		Addr               string
		Sessions           map[robust.Id]ircserver.Session
		CurrentLink        string
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               api.peerAddr,
		Sessions:           api.ircServer().GetSessions(),
		CurrentLink:        "/status/sessions",
		GetMessageRequests: api.copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (api *HTTP) handleStatusState(w http.ResponseWriter, req *http.Request) {
	textState := "state serialization failed"
	state, err := api.ircServer().Marshal(0)
	if err != nil {
		textState = fmt.Sprintf("state serialization failed: %v", err)
	} else {
		var snapshot pb.Snapshot
		if err := proto.Unmarshal(state, &snapshot); err != nil {
			textState = fmt.Sprintf("unmarshaling state failed: %v", err)
		} else {
			snapshot = privacy.FilterSnapshot(snapshot)
			var marshaler proto.TextMarshaler
			textState = marshaler.Text(&snapshot)
		}
	}

	if err := templates.ExecuteTemplate(w, "templates/state", struct {
		Addr               string
		ServerState        string
		CurrentLink        string
		Sessions           map[robust.Id]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               api.peerAddr,
		ServerState:        textState,
		CurrentLink:        "/status/state",
		Sessions:           api.ircServer().GetSessions(),
		GetMessageRequests: api.copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (api *HTTP) handleStatusIrclog(w http.ResponseWriter, req *http.Request) {
	lo, err := api.ircStore().FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	hi, err := api.ircStore().LastIndex()
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

			if err := api.ircStore().GetLog(i, l); err != nil {
				// Not every message goes into the ircStore (e.g. raft peer change
				// messages do not).
				continue
			}
			if l.Type == raft.LogCommand {
				msg := robust.NewMessageFromBytes(l.Data, robust.IdFromRaftIndex(l.Index))
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

	if err := templates.ExecuteTemplate(w, "templates/statusirclog", struct {
		Addr               string
		First              uint64
		Last               uint64
		Entries            []*raft.Log
		PrevOffset         int64
		NextOffset         uint64
		CurrentLink        string
		Sessions           map[robust.Id]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
	}{
		Addr:               api.peerAddr,
		First:              lo,
		Last:               hi,
		Entries:            entries,
		PrevOffset:         prevOffset,
		NextOffset:         lo + 50,
		CurrentLink:        "/status/irclog",
		Sessions:           api.ircServer().GetSessions(),
		GetMessageRequests: api.copyGetMessagesRequests(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (api *HTTP) handleStatus(res http.ResponseWriter, req *http.Request) {
	cfgf := api.raftNode.GetConfiguration()
	if err := cfgf.Error(); err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}
	servers := cfgf.Configuration().Servers
	p := make([]string, len(servers))
	for idx, server := range servers {
		p[idx] = string(server.Address)
	}

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
		leaderStr := string(api.raftNode.Leader())
		stats := api.raftNode.Stats()
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
			State:          api.raftNode.State().String(),
			Leader:         leaderStr,
			AppliedIndex:   appliedIndex,
			CommitIndex:    commitIndex,
			Peers:          p,
			LastContact:    api.raftNode.LastContact(),
			ExecutableHash: executablehash,
			CurrentTime:    time.Now(),
		}); err != nil {
			log.Printf("%v\n", err)
			http.Error(res, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	api.ircServer().ConfigMu.RLock()
	defer api.ircServer().ConfigMu.RUnlock()
	args := struct {
		Addr               string
		State              raft.RaftState
		Leader             string
		Peers              []string
		Stats              map[string]string
		Sessions           map[robust.Id]ircserver.Session
		GetMessageRequests map[string]GetMessagesStats
		NetConfig          config.Network
		CurrentLink        string
	}{
		Addr:               api.peerAddr,
		State:              api.raftNode.State(),
		Leader:             string(api.raftNode.Leader()),
		Peers:              p,
		Stats:              api.raftNode.Stats(),
		Sessions:           api.ircServer().GetSessions(),
		GetMessageRequests: api.copyGetMessagesRequests(),
		NetConfig:          api.ircServer().Config,
		CurrentLink:        "/status",
	}

	if err := templates.ExecuteTemplate(res, "templates/status", args); err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (api *HTTP) handleIrclog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.FormValue("sessionid"), 0, 64)
	if err != nil || id == 0 {
		http.Error(w, "Invalid session", http.StatusBadRequest)
		return
	}

	session := robust.Id{Id: id}

	// TODO(secure): pagination

	var messages []*robust.Message
	first, _ := api.ircStore().FirstIndex()
	last, _ := api.ircStore().LastIndex()
	iterator := api.ircStore().GetBulkIterator(first, last+1)
	defer iterator.Release()
	for iterator.Next() {
		elog, err := raftlog.FromBytes(iterator.Value())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if elog.Type != raft.LogCommand {
			continue
		}
		msg := robust.NewMessageFromBytes(elog.Data, robust.IdFromRaftIndex(elog.Index))
		if msg.Session.Id == session.Id {
			messages = append(messages, &msg)
		}
		output, ok := api.output().Get(msg.Id)
		if ok {
			for _, msg := range outputToRobustMessages(output) {
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
		Session  robust.Id
		Messages []*robust.Message
	}{
		session,
		messages,
	}
	if err := templates.ExecuteTemplate(w, "templates/irclog", args); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
