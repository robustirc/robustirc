package main

import (
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

	args := struct {
		Addr               string
		State              raft.RaftState
		Leader             net.Addr
		Peers              []net.Addr
		First              uint64
		Last               uint64
		Entries            []*raft.Log
		Stats              map[string]string
		Sessions           map[types.RobustId]*ircserver.Session
		GetMessageRequests map[string]GetMessageStats
	}{
		*peerAddr,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
		ircserver.Sessions,
		GetMessageRequests,
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

	s, err := ircserver.GetSession(session)
	if err != nil {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// TODO(secure): pagination

	lastSeen := s.StartId
	var messages []*types.RobustMessage
	// TODO(secure): input messages (i.e. raft log entries) which don’t result
	// in an output message in the current session (e.g. PRIVMSGs) don’t show
	// up in here at all.
	// XXX: The following code is pretty horrible. It iterates through _all_
	// log messages to create a map from id to decoded message, in order to add
	// them to the messages slice in the second loop. We should come up with a
	// better way that is lighter on resources. Perhaps store the processed
	// indexes in the session?
	inputs := make(map[types.RobustId]*types.RobustMessage)
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
		inputs[msg.Id] = &msg
	}

	for {
		if msg := ircserver.GetMessageNonBlocking(lastSeen); msg != nil {
			if msg.Type == types.RobustIRCToClient && s.InterestedIn(msg) {
				if msg.Id.Reply == 1 {
					if inputmsg, ok := inputs[types.RobustId{Id: msg.Id.Id}]; ok {
						messages = append(messages, inputmsg)
					}
				}
				messages = append(messages, msg)
			}
			lastSeen = msg.Id
		} else {
			break
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
