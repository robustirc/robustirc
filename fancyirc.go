package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
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
	"time"

	"fancyirc/ircserver"
	"fancyirc/raft_logstore"
	"fancyirc/types"

	auth "github.com/abbot/go-http-auth"
	"github.com/gorilla/mux"
	"github.com/hashicorp/raft"
	"github.com/sorcix/irc"
)

var (
	raftDir         = flag.String("raftdir", "/tmp/r", "")
	singleNode      = flag.Bool("singlenode", false, "set to true iff starting the first node for the first time")
	listen          = flag.String("listen", ":8000", "")
	join            = flag.String("join", "", "raft master to join")
	tlsCertPath     = flag.String("tls_cert_path", "", "Path to a .pem file containing the public key.")
	tlsKeyPath      = flag.String("tls_key_path", "", "Path to a .pem file containing the private key.")
	networkPassword = flag.String("network_password",
		"",
		"A (secure) password to protect the communication between raft nodes.")

	node      *raft.Raft
	peerStore *raft.JSONPeers
	logStore  *raft_logstore.FancyLogStore
)

type fancySnapshot struct {
	indexes []uint64
	store   *raft_logstore.FancyLogStore
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
	store *raft_logstore.FancyLogStore
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	msg := types.NewFancyMessageFromBytes(l.Data)
	log.Printf("Apply(fmsg.Type=%d)\n", msg.Type)

	switch msg.Type {
	case types.FancyCreateSession:
		ircserver.CreateSession(msg.Id, msg.Data)

	case types.FancyDeleteSession:
		replies := ircserver.ProcessMessage(msg.Session, irc.ParseMessage("QUIT :"+string(msg.Data)))
		ircserver.SendMessages(replies, msg.Session, msg.Id.Id)
		ircserver.DeleteSession(msg.Session)

	case types.FancyIRCFromClient:
		replies := ircserver.ProcessMessage(msg.Session, irc.ParseMessage(string(msg.Data)))
		ircserver.SendMessages(replies, msg.Session, msg.Id.Id)
	}

	return nil
}

// Snapshot returns a list of pointers based on which a snapshot can be
// created. After restoring that snapshot, the server state (current sessions,
// channels, modes, …) should be identical to the state before taking the
// snapshot. Note that entries are compacted, i.e. useless state
// transformations (think multiple nickname changes) are skipped. Also note
// that the IRC output is not guaranteed to end up the same as before. This is
// not a problem in practice as only log entries which are older than a couple
// of days are compacted, and proxy connections are only disconnected for a
// couple of minutes at a time.
func (fsm *FSM) Snapshot() (raft.FSMSnapshot, error) {
	indexes, err := fsm.store.GetAll()

	var filtered []uint64
	for i := len(indexes) - 1; i >= 0; i-- {
		var elog raft.Log

		if err := fsm.store.GetLog(indexes[i], &elog); err != nil {
			return nil, err
		}

		if elog.Type == raft.LogCommand {
			msg := types.NewFancyMessageFromBytes(elog.Data)
			if time.Since(time.Unix(0, msg.Id.Id)) > 7*24*time.Hour &&
				msg.Type == types.FancyIRCFromClient {
				ircmsg := irc.ParseMessage(msg.Data)
				// TODO: MODE (tricky)
				// Delete messages which don’t modify state.
				if ircmsg.Command == irc.PRIVMSG ||
					ircmsg.Command == irc.USER ||
					ircmsg.Command == irc.WHO ||
					ircmsg.Command == irc.QUIT {
					continue
				}
				if ircmsg.Command == irc.PART {
					// Delete all PARTs because we only keep JOINs that are still relevant.
					continue
				}
				// TODO: even better for JOIN/NICK: only retain the most recent one
				if ircmsg.Command == irc.JOIN {
					if s, ok := ircserver.GetSession(msg.Session); ok {
						// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
						if _, ok := s.Channels[ircmsg.Params[0]]; !ok {
							continue
						}
					}
				}
				if ircmsg.Command == irc.NICK {
					if s, ok := ircserver.GetSession(msg.Session); ok {
						if s.Nick != ircmsg.Params[0] {
							continue
						}
					}
				}
			}
		}

		filtered = append([]uint64{indexes[i]}, filtered...)
	}

	return &fancySnapshot{filtered, fsm.store}, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	log.Printf("Restoring snapshot\n")
	defer snap.Close()

	if err := fsm.store.DeleteAll(); err != nil {
		return err
	}

	ircserver.ClearState()

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
	master := &dnsAddr{addr}

	type joinRequest struct {
		Addr string
	}
	var buf *bytes.Buffer
	if data, err := json.Marshal(joinRequest{*listen}); err != nil {
		log.Fatal("Could not marshal join request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}
	if res, err := http.Post(fmt.Sprintf("https://%s/join", addr), "application/json", buf); err != nil {
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

// dnsAddr contains a DNS name (e.g. fancy1.twice-irc.de) and fulfills the
// net.Addr interface, so that it can be used with our raft library.
type dnsAddr struct {
	name string
}

func (a *dnsAddr) Network() string {
	return "dns"
}

func (a *dnsAddr) String() string {
	return a.name
}

// Copied from src/net/http/server.go
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

func main() {
	flag.Parse()
	log.Printf("Initializing RobustIRC…\n")

	if *networkPassword == "" {
		log.Fatalf("-network_password flag not specified. You MUST protect your network.")
	}
	digest := sha1.New()
	digest.Write([]byte(*networkPassword))
	passwordHash := "{SHA}" + base64.StdEncoding.EncodeToString(digest.Sum(nil))

	ircserver.ClearState()

	transport := NewTransport(&dnsAddr{*listen}, *networkPassword)

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

	logStore, err = raft_logstore.NewFancyLogStore(*raftDir)
	if err != nil {
		log.Fatal(err)
	}
	stablestore, err := NewFancyStableStore(*raftDir)
	if err != nil {
		log.Fatal(err)
	}
	fsm := &FSM{logStore}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, logStore, stablestore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	privaterouter := mux.NewRouter()
	privaterouter.HandleFunc("/", handleStatus)
	privaterouter.PathPrefix("/raft/").Handler(transport)
	privaterouter.HandleFunc("/join", handleJoin)
	privaterouter.HandleFunc("/snapshot", handleSnapshot)

	publicrouter := mux.NewRouter()
	publicrouter.HandleFunc("/fancyirc/v1/session", handleCreateSession).Methods("POST")
	publicrouter.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}", handleDeleteSession).Methods("DELETE")
	publicrouter.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/message", handlePostMessage).Methods("POST")
	publicrouter.HandleFunc("/fancyirc/v1/{sessionid:0x[0-9a-f]+}/messages", handleGetMessages).Methods("GET")

	a := auth.NewBasicAuthenticator("robustirc", func(user, realm string) string {
		if user == "robustirc" {
			return passwordHash
		}
		return ""
	})

	http.Handle("/fancyirc/", publicrouter)

	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username := a.CheckAuth(r); username == "" {
			a.RequireAuth(w, r)
		} else {
			privaterouter.ServeHTTP(w, r)
		}
	}))

	// Manually create the net.TCPListener so that joinMaster() does not run
	// into connection refused errors (the master will try to contact the
	// node before acknowledging the join).
	tlsconfig := &tls.Config{
		NextProtos:   []string{"http/1.1"},
		Certificates: make([]tls.Certificate, 1),
	}

	tlsconfig.Certificates[0], err = tls.LoadX509KeyPair(*tlsCertPath, *tlsKeyPath)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	tlsListener := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, tlsconfig)
	srv := http.Server{Addr: *listen}
	go srv.Serve(tlsListener)

	log.Printf("RobustIRC listening on %q. For status, see %s\n",
		*listen,
		fmt.Sprintf("https://robustirc:%s@%s/", *networkPassword, *listen))

	if *join != "" {
		p = joinMaster(*join, peerStore)
	}

	if len(p) > 0 {
		node.SetPeers(p)
	}

	for {
		time.Sleep(30 * time.Second)
		peers, err := peerStore.Peers()
		if err != nil {
			log.Fatalf("Could not get peers: %v (Peer file corrupted on disk?)\n", err)
		}
		ircserver.SendPing(node.Leader(), peers)
	}
}
