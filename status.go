package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/types"

	"github.com/hashicorp/raft"
)

//go:generate go run gentmpl.go status irclog

func handleStatus(res http.ResponseWriter, req *http.Request) {
	p, _ := peerStore.Peers()

	// robustirc-rollingrestart wants a machine-readable version of the status.
	if req.Header.Get("Accept") == "application/json" {
		type jsonStatus struct {
			State        string
			Leader       string
			Peers        []net.Addr
			AppliedIndex uint64
			CommitIndex  uint64
		}
		res.Header().Set("Content-Type", "application/json")
		leaderStr := ""
		leader := node.Leader()
		if leader != nil {
			leaderStr = leader.String()
		}
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
			State:        node.State().String(),
			Leader:       leaderStr,
			AppliedIndex: appliedIndex,
			CommitIndex:  commitIndex,
			Peers:        p,
		}); err != nil {
			log.Printf("%v\n", err)
			http.Error(res, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	lo, err := ircStore.FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}
	hi, err := ircStore.LastIndex()
	if err != nil {
		log.Printf("Could not get last index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}

	// Show the last 50 messages by default.
	if hi > 50 && hi-50 > lo {
		lo = hi - 50
	}

	if offsetStr := req.FormValue("offset"); offsetStr != "" {
		offset, err := strconv.ParseInt(offsetStr, 0, 64)
		if err != nil {
			http.Error(res, err.Error(), http.StatusBadRequest)
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
			entries = append(entries, l)
		}
	}

	prevOffset := int64(lo) - 50
	if prevOffset < 0 {
		prevOffset = 1
	}

	args := struct {
		Addr               string
		State              raft.RaftState
		Leader             net.Addr
		Peers              []net.Addr
		First              uint64
		Last               uint64
		Entries            []*raft.Log
		Stats              map[string]string
		Sessions           map[types.RobustId]ircserver.Session
		GetMessageRequests map[string]GetMessageStats
		PrevOffset         int64
		NextOffset         uint64
	}{
		*peerAddr,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
		ircServer.GetSessions(),
		GetMessageRequests,
		prevOffset,
		lo + 50,
	}

	statusTpl.Execute(res, args)
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
	for idx := first; idx <= last; idx++ {
		var elog raft.Log

		if err := ircStore.GetLog(idx, &elog); err != nil {
			// Not every message goes into the ircStore (e.g. raft peer change
			// messages do not).
			continue
		}
		if elog.Type != raft.LogCommand {
			continue
		}
		msg := types.NewRobustMessageFromBytes(elog.Data)
		if msg.Session.Id != session.Id {
			continue
		}
		messages = append(messages, &msg)
		output, ok := ircServer.Get(msg.Id)
		if ok {
			messages = append(messages, output...)
		}
	}

	args := struct {
		Session  types.RobustId
		Messages []*types.RobustMessage
	}{
		session,
		messages,
	}
	if err := irclogTpl.Execute(w, args); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
