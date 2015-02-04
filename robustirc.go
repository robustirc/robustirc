package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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
	"runtime"
	"time"

	"bitbucket.org/kardianos/osext"

	"github.com/julienschmidt/httprouter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/rafthttp"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/types"

	auth "github.com/abbot/go-http-auth"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/raft"
	"github.com/sorcix/irc"
	"github.com/stapelberg/glog"

	_ "net/http/pprof"
)

const (
	pingInterval           = 20 * time.Second
	expireSessionsInterval = 10 * time.Second
)

// XXX: when introducing a new flag, you must add it to the flag.Usage function in main().
var (
	raftDir = flag.String("raftdir",
		"/var/lib/robustirc",
		"Directory in which raft state is stored. If this directory is empty, you need to specify -join.")
	listen = flag.String("listen",
		":443",
		"[host]:port to listen on. Set to a port in the dynamic port range (49152 to 65535) and use DNS SRV records.")
	sessionExpiration = flag.Duration("session_expiration",
		30*time.Minute,
		"Time interval after which a session without any activity is terminated by the server. The client should send a PING every minute.")
	postMessageCooloff = flag.Duration("post_message_cooloff",
		500*time.Millisecond,
		"Enforced cooloff between two messages sent by a user. Set to 0 to disable throttling.")
	version = flag.Bool("version",
		false,
		"Print version and exit")

	singleNode = flag.Bool("singlenode",
		false,
		"Become a raft leader without any followers. Set to true if and only if starting the first node for the first time.")
	join = flag.String("join",
		"",
		"host:port of an existing raft node in the network that should be joined. Will also be loaded from -raftdir.")

	network = flag.String("network_name",
		"",
		`Name of the network (e.g. "robustirc.net") to use in IRC messages. Ideally also a DNS name pointing to one or more servers.`)
	peerAddr = flag.String("peer_addr",
		"",
		`host:port of this raft node (e.g. "fastbox.robustirc.net:60667"). Must be publically reachable.`)
	tlsCertPath = flag.String("tls_cert_path",
		"",
		"Path to a .pem file containing the TLS certificate.")
	tlsKeyPath = flag.String("tls_key_path",
		"",
		"Path to a .pem file containing the TLS private key.")
	tlsCAFile = flag.String("tls_ca_file",
		"",
		"Use the specified file as trusted CA instead of the system CAs. Useful for testing.")
	networkPassword = flag.String("network_password",
		"",
		"A secure password to protect the communication between raft nodes. Use pwgen(1) or similar. If empty, the ROBUSTIRC_NETWORK_PASSWORD environment variable is used.")

	node      *raft.Raft
	peerStore *raft.JSONPeers
	ircStore  *raft_store.LevelDBStore

	executablehash string = executableHash()

	// Overwritten by Makefile.
	Version = "unknown"

	isLeaderGauge = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Subsystem: "raft",
			Name:      "isleader",
			Help:      "1 if this node is the raft leader, 0 otherwise",
		},
		func() float64 {
			if node.State() == raft.Leader {
				return 1
			} else {
				return 0
			}
		},
	)
)

func init() {
	prometheus.MustRegister(isLeaderGauge)
}

type robustSnapshot struct {
	firstIndex uint64
	lastIndex  uint64
	store      *raft_store.LevelDBStore
	del        map[uint64]bool
}

func (s *robustSnapshot) canCompact(elog *raft.Log) (bool, error) {
	// TODO: compact raft messages as well, so that peer changes are not kept forever
	if elog.Type != raft.LogCommand {
		return false, nil
	}

	msg := types.NewRobustMessageFromBytes(elog.Data)

	// TODO: compact the other RobustIRC message types as well
	if msg.Type != types.RobustIRCFromClient {
		return false, nil
	}

	if time.Since(time.Unix(0, msg.Id.Id)) < 7*24*time.Hour {
		return false, nil
	}

	// We rather check if session != nil in StillRelevant callbacks where
	// necessary (some commands don’t need a session to still be relevant).
	session, _ := ircserver.GetSession(msg.Session)

	// The prev and next functions are cursors, see ircserver’s StillRelevant
	// function. They return the previous message (or next message,
	// respectively).
	get := func(index uint64) (*irc.Message, error) {
		if s.del[index] {
			return nil, nil
		}

		var nlog raft.Log
		if err := s.store.GetLog(index, &nlog); err != nil {
			return nil, err
		}

		if nlog.Type != raft.LogCommand {
			return nil, nil
		}
		nmsg := types.NewRobustMessageFromBytes(nlog.Data)
		if nmsg.Type != types.RobustIRCFromClient ||
			nmsg.Session != msg.Session {
			return nil, nil
		}
		return irc.ParseMessage(nmsg.Data), nil
	}

	prevIndex := elog.Index
	prev := func() (*irc.Message, error) {
		for {
			prevIndex--

			if prevIndex <= s.firstIndex {
				return nil, ircserver.CursorEOF
			}

			ircmsg, err := get(prevIndex)
			if err != nil {
				return nil, err
			}
			if ircmsg != nil {
				return ircmsg, nil
			}
		}
	}

	nextIndex := elog.Index
	next := func() (*irc.Message, error) {
		for {
			nextIndex++

			if nextIndex > s.lastIndex {
				return nil, ircserver.CursorEOF
			}

			ircmsg, err := get(nextIndex)
			if err != nil {
				return nil, err
			}
			if ircmsg != nil {
				return ircmsg, nil
			}
		}
	}

	relevant, err := ircserver.StillRelevant(session, irc.ParseMessage(msg.Data), prev, next)
	return !relevant, err
}

func (s *robustSnapshot) Persist(sink raft.SnapshotSink) error {
	log.Printf("Filtering and writing %d indexes\n", s.lastIndex-s.firstIndex)

	// We repeatedly compact, since the result of one compaction can affect the
	// result of other compactions (see compaction_test.go for examples).
	changed := true
	pass := 0
	for changed {
		log.Printf("Compaction pass %d\n", pass)
		pass++
		changed = false
		for i := s.firstIndex; i <= s.lastIndex; i++ {
			if s.del[i] {
				continue
			}

			var elog raft.Log

			if err := s.store.GetLog(i, &elog); err != nil {
				return err
			}

			canCompact, err := s.canCompact(&elog)
			if err != nil {
				return err
			}
			if canCompact {
				s.del[i] = true
				changed = true
			}
		}
	}

	encoder := json.NewEncoder(sink)
	for i := s.firstIndex; i <= s.lastIndex; i++ {
		if s.del[i] {
			continue
		}

		var elog raft.Log

		if err := s.store.GetLog(i, &elog); err != nil {
			return err
		}

		// TODO(secure): delete entries in s.store as well, otherwise we grow
		// without bounds. refactor compaction, perhaps moving logic into
		// ircserver/commands.go

		if err := encoder.Encode(elog); err != nil {
			sink.Cancel()
			return err
		}
	}

	sink.Close()
	return nil
}

func (s *robustSnapshot) Release() {
}

type FSM struct {
	// Used for invalidating messages of death.
	store *raft_store.LevelDBStore

	ircstore *raft_store.LevelDBStore
}

func (fsm *FSM) Apply(l *raft.Log) interface{} {
	// Skip all messages that are raft-related.
	if l.Type != raft.LogCommand {
		return nil
	}

	if err := fsm.ircstore.StoreLog(l); err != nil {
		log.Panicf("Could not persist message in irclogs/: %v", err)
	}

	msg := types.NewRobustMessageFromBytes(l.Data)
	log.Printf("Apply(fmsg.Type=%d)\n", msg.Type)

	switch msg.Type {
	case types.RobustMessageOfDeath:
		// To prevent the message from being accepted again.
		ircserver.UpdateLastMessage(&msg, l.Data)
		log.Printf("Skipped message of death.\n")

	case types.RobustCreateSession:
		ircserver.CreateSession(msg.Id, msg.Data)

	case types.RobustDeleteSession:
		replies := ircserver.ProcessMessage(msg.Session, irc.ParseMessage("QUIT :"+string(msg.Data)))
		ircserver.SendMessages(replies, msg.Session, msg.Id.Id)
		ircserver.DeleteSession(msg.Session)

	case types.RobustIRCFromClient:
		defer func() {
			if r := recover(); r != nil {
				// Panics in ircserver.ProcessMessage() are a problem, since
				// they will bring down the entire raft cluster and you cannot
				// bring up any raft node anymore without deleting the entire
				// log.
				//
				// Therefore, when we panic, we invalidate the log entry in
				// question before crashing. This doesn’t fix the underlying
				// bug, i.e. an IRC message will then go unhandled, but it
				// prevents RobustIRC from dying horribly in such a situation.
				msg.Type = types.RobustMessageOfDeath
				data, err := json.Marshal(msg)
				if err != nil {
					log.Panicf("Could not marshal message: %v", err)
				}
				l.Data = data
				if err := fsm.store.StoreLog(l); err != nil {
					log.Panicf("Could not store log while marking message as message of death: %v", err)
				}
				log.Printf("Marked %+v as message of death\n", l)
				panic(r)
			}
		}()

		// Need to do this first, because ircserver.ProcessMessage could delete
		// the session, e.g. by using KILL or QUIT.
		if err := ircserver.UpdateLastMessage(&msg, l.Data); err != nil {
			log.Printf("Error updating the last message for session: %v\n", err)
		}
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
	first, err := fsm.ircstore.FirstIndex()
	if err != nil {
		return nil, err
	}

	last, err := fsm.ircstore.LastIndex()
	if err != nil {
		return nil, err
	}
	return &robustSnapshot{
		first,
		last,
		fsm.ircstore,
		make(map[uint64]bool),
	}, err
}

func (fsm *FSM) Restore(snap io.ReadCloser) error {
	log.Printf("Restoring snapshot\n")
	defer snap.Close()

	// Clear state by resetting the ircserver package’s state and deleting the
	// entire ircstore. Snapshots contain the entire (possibly compacted)
	// ircstore, so this is safe.
	min, err := fsm.ircstore.FirstIndex()
	if err != nil {
		return err
	}
	max, err := fsm.ircstore.LastIndex()
	if err != nil {
		return err
	}
	if err := fsm.ircstore.DeleteRange(min, max); err != nil {
		return err
	}

	ircserver.ClearState()

	decoder := json.NewDecoder(snap)
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
	if data, err := json.Marshal(joinRequest{*peerAddr}); err != nil {
		log.Fatal("Could not marshal join request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}

	client := robustClient()
	req, err := http.NewRequest("POST", fmt.Sprintf("https://%s/join", addr), buf)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if res, err := client.Do(req); err != nil {
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

func executableHash() string {
	path, err := osext.Executable()
	if err != nil {
		log.Fatal(err)
	}

	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		log.Fatal(err)
	}

	return fmt.Sprintf("%.16x", h.Sum(nil))
}

// dnsAddr contains a DNS name (e.g. robust1.twice-irc.de) and fulfills the
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

func printDefault(f *flag.Flag) {
	format := "  -%s=%s: %s\n"
	if getter, ok := f.Value.(flag.Getter); ok {
		if _, ok := getter.Get().(string); ok {
			// put quotes on the value
			format = "  -%s=%q: %s\n"
		}
	}
	fmt.Fprintf(os.Stderr, format, f.Name, f.DefValue, f.Usage)
}

func main() {
	flag.Usage = func() {
		// It is unfortunate that we need to re-implement flag.PrintDefaults(),
		// but I cannot see any other way to achieve the grouping of flags.
		fmt.Fprintf(os.Stderr, "RobustIRC server (= node)\n")
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "The following flags are REQUIRED:\n")
		printDefault(flag.Lookup("network_name"))
		printDefault(flag.Lookup("network_password"))
		printDefault(flag.Lookup("peer_addr"))
		printDefault(flag.Lookup("tls_cert_path"))
		printDefault(flag.Lookup("tls_key_path"))
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "The following flags are only relevant when bootstrapping the network (once):\n")
		printDefault(flag.Lookup("join"))
		printDefault(flag.Lookup("singlenode"))
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "The following flags are optional:\n")
		printDefault(flag.Lookup("listen"))
		printDefault(flag.Lookup("raftdir"))
		printDefault(flag.Lookup("tls_ca_file"))
		printDefault(flag.Lookup("version"))
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "The following flags are optional and provided by glog:\n")
		printDefault(flag.Lookup("alsologtostderr"))
		printDefault(flag.Lookup("log_backtrace_at"))
		printDefault(flag.Lookup("log_dir"))
		printDefault(flag.Lookup("log_total_bytes"))
		printDefault(flag.Lookup("logtostderr"))
		printDefault(flag.Lookup("stderrthreshold"))
		printDefault(flag.Lookup("v"))
		printDefault(flag.Lookup("vmodule"))
	}
	flag.Parse()

	// Store logs in -raftdir, unless otherwise specified.
	if flag.Lookup("log_dir").Value.String() == "" {
		flag.Set("log_dir", *raftDir)
	}

	defer glog.Flush()
	glog.MaxSize = 64 * 1024 * 1024
	glog.CopyStandardLogTo("INFO")

	if *version {
		log.Printf("RobustIRC %s\n", Version)
		return
	}

	if _, err := os.Stat(filepath.Join(*raftDir, "deletestate")); err == nil {
		if err := os.RemoveAll(*raftDir); err != nil {
			log.Fatal(err)
		}
		if err := os.Mkdir(*raftDir, 0700); err != nil {
			log.Fatal(err)
		}
		log.Printf("Deleted %q because %q existed\n", *raftDir, filepath.Join(*raftDir, "deletestate"))
	}

	log.Printf("Initializing RobustIRC…\n")

	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	if *networkPassword == "" {
		*networkPassword = os.Getenv("ROBUSTIRC_NETWORK_PASSWORD")
	}
	if *networkPassword == "" {
		log.Fatalf("-network_password not set. You MUST protect your network.\n")
	}
	digest := sha1.New()
	digest.Write([]byte(*networkPassword))
	passwordHash := "{SHA}" + base64.StdEncoding.EncodeToString(digest.Sum(nil))

	if *network == "" {
		log.Fatalf("-network_name not set, but required.\n")
	}
	ircserver.ServerPrefix = &irc.Prefix{Name: *network}

	if *peerAddr == "" {
		log.Printf("-peer_addr not set, initializing to %q. Make sure %q is a host:port string that other raft nodes can connect to!\n", *listen, *listen)
		*peerAddr = *listen
	}

	ircserver.ClearState()
	ircserver.NetworkPassword = *networkPassword

	transport := rafthttp.NewHTTPTransport(
		&dnsAddr{*peerAddr},
		robustClient(),
		nil,
		"")

	peerStore = raft.NewJSONPeers(*raftDir, transport)

	var p []net.Addr

	config := raft.DefaultConfig()
	config.Logger = log.New(glog.LogBridgeFor("INFO"), "", log.Lshortfile)
	if *singleNode {
		config.EnableSingleNode = true
	}

	// Keep 5 snapshots in *raftDir/snapshots, log to stderr.
	fss, err := raft.NewFileSnapshotStore(*raftDir, 5, nil)
	if err != nil {
		log.Fatal(err)
	}

	// TODO(secure): update once https://github.com/hashicorp/raft/issues/32 gets closed.
	// Check whether a snapshot needs to be taken every second. The check is
	// cheap, and the default value far too high for networks with a high
	// number of messages/s.
	config.SnapshotInterval = 1 * time.Second

	// Batch as many messages as possible into a single appendEntries RPC.
	// There is no downside to setting this too high.
	config.MaxAppendEntries = 1024

	// It could be that the heartbeat goroutine is not scheduled for a while,
	// so relax the default of 500ms.
	config.LeaderLeaseTimeout = 1 * time.Second

	// We use prometheus, so hook up the metrics package (used by raft) to
	// prometheus as well.
	sink, err := metrics.NewPrometheusSink()
	if err != nil {
		log.Fatal(err)
	}
	metrics.NewGlobal(metrics.DefaultConfig("raftmetrics"), sink)

	logStore, err := raft_store.NewLevelDBStore(filepath.Join(*raftDir, "raftlog"))
	if err != nil {
		log.Fatal(err)
	}
	ircStore, err = raft_store.NewLevelDBStore(filepath.Join(*raftDir, "irclog"))
	if err != nil {
		log.Fatal(err)
	}
	fsm := &FSM{logStore, ircStore}
	logcache, err := raft.NewLogCache(config.MaxAppendEntries, logStore)
	if err != nil {
		log.Fatal(err)
	}

	// NewRaft(*Config, FSM, LogStore, StableStore, SnapshotStore, PeerStore, Transport)
	node, err = raft.NewRaft(config, fsm, logcache, logStore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: observeleaderchanges?
	// TODO: observenexttime?

	privaterouter := httprouter.New()
	privaterouter.HandlerFunc("GET", "/", handleStatus)
	privaterouter.HandlerFunc("GET", "/irclog", handleIrclog)
	privaterouter.Handler("POST", "/raft/*rest", transport)
	privaterouter.HandlerFunc("POST", "/join", handleJoin)
	privaterouter.HandlerFunc("GET", "/snapshot", handleSnapshot)
	privaterouter.HandlerFunc("GET", "/leader", handleLeader)
	privaterouter.HandlerFunc("GET", "/executablehash", handleHash)
	privaterouter.HandlerFunc("POST", "/quit", handleQuit)
	privaterouter.Handler("GET", "/metrics", prometheus.Handler())

	publicrouter := httprouter.New()
	publicrouter.Handle("POST", "/robustirc/v1/:sessionid", handleCreateSession)
	publicrouter.Handle("POST", "/robustirc/v1/:sessionid/message", handlePostMessage)
	publicrouter.Handle("GET", "/robustirc/v1/:sessionid/messages", handleGetMessages)
	publicrouter.Handle("DELETE", "/robustirc/v1/:sessionid", handleDeleteSession)

	a := auth.NewBasicAuthenticator("robustirc", func(user, realm string) string {
		if user == "robustirc" {
			return passwordHash
		}
		return ""
	})

	http.Handle("/robustirc/", publicrouter)

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
		*peerAddr,
		fmt.Sprintf("https://robustirc:%s@%s/", *networkPassword, *peerAddr))

	if *join != "" {
		p = joinMaster(*join, peerStore)
		// TODO(secure): properly handle joins on the server-side where the joining node is already in the network.
	}

	if len(p) > 0 {
		node.SetPeers(p)
	}

	pingTimer := time.After(pingInterval)
	expireSessionsTimer := time.After(expireSessionsInterval)
	for {
		select {
		case <-pingTimer:
			pingTimer = time.After(pingInterval)
			peers, err := peerStore.Peers()
			if err != nil {
				log.Fatalf("Could not get peers: %v (Peer file corrupted on disk?)\n", err)
			}
			ircserver.SendPing(node.Leader(), peers)

		case <-expireSessionsTimer:
			expireSessionsTimer = time.After(expireSessionsInterval)

			// Race conditions (a node becoming a leader or ceasing to be the
			// leader shortly before/after this runs) are okay, since the timer
			// is triggered often enough on every node so that it will
			// eventually run on the leader.
			if node.State() != raft.Leader {
				continue
			}

			for id, s := range ircserver.Sessions {
				if time.Since(s.LastActivity) <= *sessionExpiration {
					continue
				}

				log.Printf("Expiring session %v\n", id)

				msg := types.NewRobustMessage(types.RobustDeleteSession, id, fmt.Sprintf("Ping timeout (%v)", *sessionExpiration))
				// Cannot fail, no user input.
				msgbytes, _ := json.Marshal(msg)

				f := node.Apply(msgbytes, 10*time.Second)
				if err := f.Error(); err != nil {
					log.Printf("Apply(): %v\n", err)
					break
				}
			}
		}
	}
}
