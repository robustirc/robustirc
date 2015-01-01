package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fancyirc/types"

	"github.com/gorilla/mux"
	"github.com/hashicorp/raft"
	"github.com/sorcix/irc"
)

var (
	raftDir    = flag.String("raftdir", "/tmp/r", "")
	singleNode = flag.Bool("singlenode", false, "set to true iff starting the first node for the first time")
	listen     = flag.String("listen", ":8000", "")
	join       = flag.String("join", "", "raft master to join")
	network    = flag.String("network", "fancy.twice-irc.de", "Name of the network. Ideally also a DNS name pointing to one or more servers.")
	newEvent   = sync.NewCond(&sync.Mutex{})

	node      *raft.Raft
	peerStore *raft.JSONPeers
	logStore  *fancyLogStore

	sessionMu sync.Mutex
	sessions  = make(map[types.FancyId]*Session)
)

type Session struct {
	Id       types.FancyId
	Nick     string
	Channels map[string]bool
}

func (s *Session) ircPrefix() *irc.Prefix {
	// TODO(secure): Is there a better value for User?
	return &irc.Prefix{
		Name: s.Nick,
		User: "fancy",
		Host: fmt.Sprintf("fancy/0x%x", s.Id),
	}
}

func (s *Session) interestedIn(msg types.FancyMessage) bool {
	log.Printf("[DEBUG] Checking whether %+v is interested in %+v\n", s, msg)
	ircmsg := irc.ParseMessage(msg.Data)
	serverPrefix := irc.Prefix{Name: *network}

	if *ircmsg.Prefix == serverPrefix && msg.Session == s.Id {
		return true
	}

	switch ircmsg.Command {
	case irc.NICK:
		// TODO(secure): does it make sense to restrict this to sessions which
		// have a channel in common? noting this because it doesn’t handle the
		// query-only use-case. if there’s no downside (except for the privacy
		// aspect), perhaps leave it as-is?
		return true
	case irc.JOIN:
		return s.Channels[ircmsg.Trailing]
	case irc.PRIVMSG:
		return *s.ircPrefix() != *ircmsg.Prefix && s.Channels[ircmsg.Params[0]]
	default:
		return false
	}
}

type fancySnapshot struct {
	indexes []uint64
	store   *fancyLogStore
}

func (s *fancySnapshot) Persist(sink raft.SnapshotSink) error {
	log.Printf("Persisting indexes %v\n", s.indexes)
	encoder := gob.NewEncoder(sink)
	var entry raft.Log
	for _, index := range s.indexes {
		if err := s.store.GetLog(index, &entry); err != nil {
			sink.Cancel()
			return err
		}
		if err := encoder.Encode(entry); err != nil {
			sink.Cancel()
			return err
		}
	}
	sink.Close()
	return nil
}

func (s *fancySnapshot) Release() {
}

type FSM struct {
	store *fancyLogStore
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	var replies []irc.Message

	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	msg := types.NewFancyMessageFromBytes(l.Data)
	log.Printf("Apply(fmsg.Type=%d)\n", msg.Type)
	if msg.Type == types.FancyCreateSession {
		sessions[msg.Id] = &Session{
			Id:       msg.Id,
			Channels: make(map[string]bool),
		}
		log.Printf("sessions now: %+v\n", sessions)
	}

	if msg.Type == types.FancyIRCFromClient {
		message := irc.ParseMessage(string(msg.Data))
		s := sessions[msg.Session]

		switch message.Command {
		case irc.NICK:
			oldPrefix := s.ircPrefix()
			s.Nick = message.Params[0]
			// TODO(secure): when connecting, handle nickname already in use.
			// TODO(secure): handle nickname already in use.
			log.Printf("nickname now: %+v\n", s)
			if !strings.HasPrefix(oldPrefix.String(), ":!") {
				replies = append(replies, irc.Message{
					Prefix:   oldPrefix,
					Command:  irc.NICK,
					Trailing: message.Params[0],
				})
			}

		case irc.USER:
			// TODO(secure): send 002, 003, 004, 005, 251, 252, 254, 255, 265, 266, [motd = 375, 372, 376]
			replies = append(replies, irc.Message{
				Prefix:   &irc.Prefix{Name: *network},
				Command:  irc.RPL_WELCOME,
				Params:   []string{s.Nick},
				Trailing: "Welcome to fancyirc :)",
			})

		case irc.JOIN:
			// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
			channel := message.Params[0]
			s.Channels[channel] = true
			var nicks []string
			// TODO(secure): a separate map for quick lookup may be worthwhile for big channels.
			for _, session := range sessions {
				if !session.Channels[channel] {
					continue
				}
				nicks = append(nicks, session.Nick)
			}
			replies = append(replies, irc.Message{
				Prefix:   s.ircPrefix(),
				Command:  irc.JOIN,
				Trailing: channel,
			})
			//replies = append(replies, irc.Message{
			//	Prefix:  &irc.Prefix{Name: *network},
			//	Command: irc.RPL_NOTOPIC,
			//	Params:  []string{channel},
			//})
			// TODO(secure): why the = param?
			replies = append(replies, irc.Message{
				Prefix:   &irc.Prefix{Name: *network},
				Command:  irc.RPL_NAMREPLY,
				Params:   []string{s.Nick, "=", channel},
				Trailing: strings.Join(nicks, " "),
			})
			replies = append(replies, irc.Message{
				Prefix:   &irc.Prefix{Name: *network},
				Command:  irc.RPL_ENDOFNAMES,
				Params:   []string{s.Nick, channel},
				Trailing: "End of /NAMES list.",
			})

		case irc.PRIVMSG:
			replies = append(replies, irc.Message{
				Prefix:   s.ircPrefix(),
				Command:  irc.PRIVMSG,
				Params:   []string{message.Params[0]},
				Trailing: message.Trailing,
			})
		}
	}

	log.Printf("TODO: apply %v\n", l)
	newEvent.Broadcast()
	return replies
}

func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	indexes, err := fsm.store.GetAll()
	return &fancySnapshot{indexes, fsm.store}, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	log.Printf("Restoring snapshot\n")
	defer snap.Close()

	if err := os.RemoveAll(filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	if err := os.Rename(filepath.Join(*raftDir, "fancylogs"),
		filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancylogs"), 0755); err != nil {
		return err
	}

	decoder := gob.NewDecoder(snap)
	for {
		var entry raft.Log
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// TODO(secure): is it okay to re-apply these entries? i.e., when
		// restoring snapshots during normal operation (when does that
		// happen?), will we re-send messages to clients?
		fsm.Apply(&entry)

		if err := fsm.store.StoreLog(&entry); err != nil {
			return err
		}
	}

	if err := os.RemoveAll(filepath.Join(*raftDir, "fancylogs-obsolete")); err != nil {
		return err
	}

	log.Printf("Restored snapshot\n")

	return nil
}

func joinMaster(addr string, peerStore *raft.JSONPeers) []net.Addr {
	master, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		log.Fatalf("Could not resolve %q: %v", addr, err)
	}

	type joinRequest struct {
		Addr string
	}
	var buf *bytes.Buffer
	if data, err := json.Marshal(joinRequest{*listen}); err != nil {
		log.Fatal("Could not marshal join request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}
	if res, err := http.Post(fmt.Sprintf("http://%s/join", addr), "application/json", buf); err != nil {
		log.Fatal("Could not send join request:", err)
	} else if res.StatusCode > 399 {
		data, _ := ioutil.ReadAll(res.Body)
		log.Fatal("Join request failed:", string(data))
	} else if res.StatusCode > 299 {
		loc := res.Header.Get("Location")
		if loc == "" {
			log.Fatal("Redirect has no Location header")
		}
		u, err := url.Parse(loc)
		if err != nil {
			log.Fatalf("Could not parse redirection %q: %v", loc, err)
		}

		return joinMaster(u.Host, peerStore)
	}

	log.Printf("Adding master %v as peer\n", master)
	p, err := peerStore.Peers()
	if err != nil {
		log.Fatal("Could not read peers:", err)
	}
	p = raft.AddUniquePeer(p, master)
	peerStore.SetPeers(p)
	return p
}

func main() {
	flag.Parse()
	log.Printf("hey\n")

	a, err := net.ResolveTCPAddr("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	transport := NewTransport(a)
	http.Handle("/raft/", transport)

	peerStore = raft.NewJSONPeers(*raftDir, transport)

	var p []net.Addr

	config := raft.DefaultConfig()
	if *singleNode {
		config.EnableSingleNode = true
	}

	// Keep 5 snapshots in *raftDir/snapshots, log to stderr.
	fss, err := raft.NewFileSnapshotStore(*raftDir, 5, nil)
	if err != nil {
		log.Fatal(err)
	}

	// TODO(secure): remove this, it’s only for forcing many snapshots right now.
	config.SnapshotThreshold = 2
	config.SnapshotInterval = 1 * time.Second

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancylogs"), 0755); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(*raftDir, "fancystable"), 0755); err != nil {
		log.Fatal(err)
	}

	logStore = &fancyLogStore{}
	stablestore := &fancyStableStore{}
	fsm := &FSM{logStore}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, logStore, stablestore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	r := mux.NewRouter()
	r.HandleFunc("/", handleStatus)
	r.HandleFunc("/join", handleJoin)
	r.HandleFunc("/snapshot", handleSnapshot)
	r.HandleFunc("/fancyirc/v1/session", handleCreateSession).Methods("POST")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}", handleDeleteSession).Methods("DELETE")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/message", handlePostMessage).Methods("POST")
	r.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/messages", handleGetMessages).Methods("GET")
	http.Handle("/", r)

	go func() {
		err := http.ListenAndServe(*listen, nil)
		if err != nil {
			log.Fatal(err)
		}
	}()

	if *join != "" {
		p = joinMaster(*join, peerStore)
	}

	if len(p) > 0 {
		node.SetPeers(p)
	}

	for {

		time.Sleep(1000 * time.Millisecond)

		// err = s.raft.Add(b, raftMaxTime)
		// if err != nil {
		// 	eWrapper.Done(false)
		// }

		// eWrapper.Done(true)

	}
}
