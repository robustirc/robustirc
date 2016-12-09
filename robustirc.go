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
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/julienschmidt/httprouter"
	"github.com/kardianos/osext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/bridge/tlsutil"
	"github.com/robustirc/rafthttp"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/outputstream"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/timesafeguard"
	"github.com/robustirc/robustirc/types"

	auth "github.com/abbot/go-http-auth"
	"github.com/armon/go-metrics"
	metrics_prometheus "github.com/armon/go-metrics/prometheus"
	"github.com/hashicorp/raft"
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
	version = flag.Bool("version",
		false,
		"Print version and exit")

	singleNode = flag.Bool("singlenode",
		false,
		"Become a raft leader without any followers. Set to true if and only if starting the first node for the first time.")
	join = flag.String("join",
		"",
		"host:port of an existing raft node in the network that should be joined. Will also be loaded from -raftdir.")
	dumpCanaryState = flag.String("dump_canary_state",
		"",
		"If specified, initializes the raft node (from a snapshot), then dumps all message state to the specified file. To be used via robustirc-canary.")
	dumpHeapProfile = flag.String("dump_heap_profile",
		"",
		"If specified, a heap profile will be dumped to the specified file. Only relevant when -dump_canary_state is set.")
	canaryCompactionStart = flag.Int64("canary_compaction_start",
		0,
		"If > 0, a nanosecond precision UNIX timestamp of when the compaction was started (for deterministic results across runs).")

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
	networkPassword = flag.String("network_password",
		"",
		"A secure password to protect the communication between raft nodes. Use pwgen(1) or similar. If empty, the ROBUSTIRC_NETWORK_PASSWORD environment variable is used.")

	node      *raft.Raft
	peerStore *raft.JSONPeers
	ircStore  *raft_store.LevelDBStore
	ircServer *ircserver.IRCServer

	executablehash = executableHash()

	// Version is overwritten by Makefile.
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
			}
			return 0
		},
	)

	sessionsGauge = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Subsystem: "irc",
			Name:      "sessions",
			Help:      "Number of IRC sessions",
		},
		func() float64 {
			return float64(ircServer.NumSessions())
		},
	)

	appliedMessages = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "applied_messages",
			Help: "How many raft messages were applied, partitioned by message type",
		},
		[]string{"type"},
	)

	secondsInState = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "seconds_in_state",
			Help: "How many seconds the node was in each raft state",
		},
		[]string{"state"},
	)
)

func init() {
	prometheus.MustRegister(isLeaderGauge)
	prometheus.MustRegister(sessionsGauge)
	prometheus.MustRegister(appliedMessages)
	prometheus.MustRegister(secondsInState)
}

func joinMaster(addr string, peerStore *raft.JSONPeers) []string {
	type joinRequest struct {
		Addr string
	}
	var buf *bytes.Buffer
	if data, err := json.Marshal(joinRequest{*peerAddr}); err != nil {
		log.Fatal("Could not marshal join request:", err)
	} else {
		buf = bytes.NewBuffer(data)
	}

	client := robusthttp.Client(*networkPassword, true)
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

	log.Printf("Adding master %q as peer\n", addr)
	p, err := peerStore.Peers()
	if err != nil {
		log.Fatal("Could not read peers:", err)
	}
	p = raft.AddUniquePeer(p, addr)
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

// XXX(1.0): delete this function as users are expected to have upgraded.
func deleteOldCompactionDatabases(tmpdir string) error {
	dir, err := os.Open(tmpdir)
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name, "permanent-compaction.sqlite3") {
			if err := os.Remove(filepath.Join(tmpdir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyMessage applies the specified message to the network via Raft.
func applyMessage(msg *types.RobustMessage, timeout time.Duration) (raft.ApplyFuture, error) {
	applyMu.Lock()
	defer applyMu.Unlock()

	msg.Id = ircServer.NewRobustMessageId()
	msgbytes, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	return node.Apply(msgbytes, timeout), nil
}

func applyMessageWait(msg *types.RobustMessage, timeout time.Duration) error {
	f, err := applyMessage(msg, timeout)
	if err != nil {
		return err
	}
	if err := f.Error(); err != nil {
		return err
	}
	if err, ok := f.Response().(error); ok {
		return err
	}
	return nil
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

// exitOnRecover is used to circumvent the recover handler that net/http
// installs. We need to exit in order to get restarted by the init
// system/supervisor and get into a clean state again.
func exitOnRecover() {
	if r := recover(); r != nil {
		// This mimics go/src/net/http/server.go.
		const size = 64 << 10
		buf := make([]byte, size)
		buf = buf[:runtime.Stack(buf, false)]
		glog.Errorf("http: panic serving: %v\n%s", r, buf)
		glog.Flush()
		os.Exit(1)
	}
}

func exitOnRecoverHandleFunc(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer exitOnRecover()
		h(w, r)
	})
}

func exitOnRecoverHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer exitOnRecover()
		h.ServeHTTP(w, r)
	})
}

func exitOnRecoverHandle(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		defer exitOnRecover()
		h(w, r, ps)
	}
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
		printDefault(flag.Lookup("dump_canary_state"))
		printDefault(flag.Lookup("dump_heap_profile"))
		printDefault(flag.Lookup("canary_compaction_start"))
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

	log.Printf("RobustIRC %s\n", Version)
	if *version {
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

	if err := outputstream.DeleteOldDatabases(*raftDir); err != nil {
		log.Fatalf("Could not delete old outputstream databases: %v\n", err)
	}

	if err := deleteOldCompactionDatabases(*raftDir); err != nil {
		glog.Errorf("Could not delete old compaction databases: %v (ignoring)\n", err)
	}

	log.Printf("Initializing RobustIRC…\n")

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

	if *peerAddr == "" {
		log.Printf("-peer_addr not set, initializing to %q. Make sure %q is a host:port string that other raft nodes can connect to!\n", *listen, *listen)
		*peerAddr = *listen
	}

	ircServer = ircserver.NewIRCServer(*raftDir, *network, time.Now())

	transport := rafthttp.NewHTTPTransport(
		*peerAddr,
		// Not deadlined, otherwise snapshot installments fail.
		robusthttp.Client(*networkPassword, false),
		nil,
		"")

	peerStore = raft.NewJSONPeers(*raftDir, transport)

	if *join == "" && !*singleNode {
		peers, err := peerStore.Peers()
		if err != nil {
			log.Fatal(err.Error())
		}
		if len(peers) == 0 {
			if !*timesafeguard.DisableTimesafeguard {
				log.Fatalf("No peers known and -join not specified. Joining the network is not safe because timesafeguard cannot be called.\n")
			}
		} else {
			if len(peers) == 1 && peers[0] == *peerAddr {
				// To prevent crashlooping too frequently in case the init system directly restarts our process.
				time.Sleep(10 * time.Second)
				log.Fatalf("Only known peer is myself (%q), implying this node was removed from the network. Please kill the process and remove the data.\n", *peerAddr)
			}
			if err := timesafeguard.SynchronizedWithNetwork(*peerAddr, peers, *networkPassword); err != nil {
				log.Fatal(err.Error())
			}
		}
	}

	var p []string

	config := raft.DefaultConfig()
	config.Logger = log.New(glog.LogBridgeFor("INFO"), "", log.Lshortfile)
	if *singleNode {
		config.EnableSingleNode = true
		config.StartAsLeader = true
	}

	// Keep 5 snapshots in *raftDir/snapshots, log to stderr.
	fss, err := raft.NewFileSnapshotStoreWithLogger(*raftDir, 5, config.Logger)
	if err != nil {
		log.Fatal(err)
	}

	// How often to check whether a snapshot should be taken. The check is
	// cheap, and the default value far too high for networks with a high
	// number of messages/s.
	// At the same time, it is important that we don’t check too early,
	// otherwise recovering from the most recent snapshot doesn’t work because
	// after recovering, a new snapshot (over the 0 committed messages) will be
	// taken immediately, effectively overwriting the result of the snapshot
	// recovery.
	config.SnapshotInterval = 300 * time.Second

	// Batch as many messages as possible into a single appendEntries RPC.
	// There is no downside to setting this too high.
	config.MaxAppendEntries = 1024

	// It could be that the heartbeat goroutine is not scheduled for a while,
	// so relax the default of 500ms.
	config.LeaderLeaseTimeout = timesafeguard.ElectionTimeout
	config.HeartbeatTimeout = timesafeguard.ElectionTimeout
	config.ElectionTimeout = timesafeguard.ElectionTimeout

	// We use prometheus, so hook up the metrics package (used by raft) to
	// prometheus as well.
	sink, err := metrics_prometheus.NewPrometheusSink()
	if err != nil {
		log.Fatal(err)
	}
	metrics.NewGlobal(metrics.DefaultConfig("raftmetrics"), sink)

	bootstrapping := *singleNode || *join != ""
	logStore, err := raft_store.NewLevelDBStore(filepath.Join(*raftDir, "raftlog"), bootstrapping)
	if err != nil {
		log.Fatal(err)
	}
	ircStore, err = raft_store.NewLevelDBStore(filepath.Join(*raftDir, "irclog"), bootstrapping)
	if err != nil {
		log.Fatal(err)
	}
	fsm := &FSM{
		store:             logStore,
		ircstore:          ircStore,
		lastSnapshotState: make(map[uint64][]byte),
	}
	logcache, err := raft.NewLogCache(config.MaxAppendEntries, logStore)
	if err != nil {
		log.Fatal(err)
	}

	node, err = raft.NewRaft(config, fsm, logcache, logStore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	if *dumpCanaryState != "" {
		canary(fsm, *dumpCanaryState)
		if *dumpHeapProfile != "" {
			debug.FreeOSMemory()
			f, err := os.Create(*dumpHeapProfile)
			if err != nil {
				log.Fatal(err)
			}
			defer f.Close()
			pprof.WriteHeapProfile(f)
		}
		return
	}

	go func() {
		for {
			secondsInState.WithLabelValues(node.State().String()).Inc()
			time.Sleep(1 * time.Second)
		}
	}()

	privaterouter := httprouter.New()
	privaterouter.Handler("GET", "/", exitOnRecoverHandleFunc(handleStatus))
	privaterouter.Handler("GET", "/irclog", exitOnRecoverHandleFunc(handleIrclog))
	privaterouter.Handler("POST", "/raft/*rest", exitOnRecoverHandler(transport))
	privaterouter.Handler("POST", "/join", exitOnRecoverHandleFunc(handleJoin))
	privaterouter.Handler("POST", "/part", exitOnRecoverHandleFunc(handlePart))
	privaterouter.Handler("GET", "/snapshot", exitOnRecoverHandleFunc(handleSnapshot))
	privaterouter.Handler("GET", "/leader", exitOnRecoverHandleFunc(handleLeader))
	privaterouter.Handler("POST", "/quit", exitOnRecoverHandleFunc(handleQuit))
	privaterouter.Handler("GET", "/config", exitOnRecoverHandleFunc(handleGetConfig))
	privaterouter.Handler("POST", "/config", exitOnRecoverHandleFunc(handlePostConfig))
	privaterouter.Handler("GET", "/metrics", exitOnRecoverHandler(prometheus.Handler()))

	publicrouter := httprouter.New()
	publicrouter.Handle("POST", "/robustirc/v1/:sessionid", exitOnRecoverHandle(handleCreateSession))
	publicrouter.Handle("POST", "/robustirc/v1/:sessionid/message", exitOnRecoverHandle(handlePostMessage))
	publicrouter.Handle("GET", "/robustirc/v1/:sessionid/messages", exitOnRecoverHandle(handleGetMessages))
	publicrouter.Handle("DELETE", "/robustirc/v1/:sessionid", exitOnRecoverHandle(handleDeleteSession))

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

	srv := http.Server{Addr: *listen}
	if err := http2.ConfigureServer(&srv, nil); err != nil {
		log.Fatal(err)
	}

	// Manually create the net.TCPListener so that joinMaster() does not run
	// into connection refused errors (the master will try to contact the
	// node before acknowledging the join).
	kpr, err := tlsutil.NewKeypairReloader(*tlsCertPath, *tlsKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	srv.TLSConfig.GetCertificate = kpr.GetCertificateFunc()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	tlsListener := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, srv.TLSConfig)
	go srv.Serve(tlsListener)

	log.Printf("RobustIRC listening on %q. For status, see %s\n",
		*peerAddr,
		fmt.Sprintf("https://robustirc:%s@%s/", *networkPassword, *peerAddr))

	if *join != "" {
		if err := timesafeguard.SynchronizedWithMasterAndNetwork(*peerAddr, *join, *networkPassword); err != nil {
			log.Fatal(err.Error())
		}

		p = joinMaster(*join, peerStore)
		// TODO(secure): properly handle joins on the server-side where the joining node is already in the network.
	}

	if len(p) > 0 {
		node.SetPeers(p)
	}

	expireSessionsTimer := time.After(expireSessionsInterval)
	secondTicker := time.Tick(1 * time.Second)
	for {
		select {
		case <-secondTicker:
			if node.State() == raft.Shutdown {
				log.Fatal("Node removed from the network (in raft state shutdown), terminating.")
			}
		case <-expireSessionsTimer:
			expireSessionsTimer = time.After(expireSessionsInterval)

			// Race conditions (a node becoming a leader or ceasing to be the
			// leader shortly before/after this runs) are okay, since the timer
			// is triggered often enough on every node so that it will
			// eventually run on the leader.
			if node.State() != raft.Leader {
				continue
			}

			for _, msg := range ircServer.ExpireSessions() {
				if err := applyMessageWait(msg, 10*time.Second); err != nil {
					log.Printf("Apply(): %v", err)
				}
			}
		}
	}
}
