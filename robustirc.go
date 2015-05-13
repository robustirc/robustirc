package main

// Generate errors.go which is used in canCompact() below.
//go:generate go run generrors.go

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
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/kardianos/osext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robustirc/rafthttp"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/raft_store"
	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/types"

	auth "github.com/abbot/go-http-auth"
	"github.com/armon/go-metrics"
	metrics_prometheus "github.com/armon/go-metrics/prometheus"
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
	version = flag.Bool("version",
		false,
		"Print version and exit")

	singleNode = flag.Bool("singlenode",
		false,
		"Become a raft leader without any followers. Set to true if and only if starting the first node for the first time.")
	join = flag.String("join",
		"",
		"host:port of an existing raft node in the network that should be joined. Will also be loaded from -raftdir.")
	canaryReport = flag.String("canary_report",
		"",
		"If specified, all messages on the node specified by -join will be processed locally and a report about the differences is stored in the path given by -canary_report")

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

	netConfig = config.DefaultConfig

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
)

func init() {
	prometheus.MustRegister(isLeaderGauge)
	prometheus.MustRegister(sessionsGauge)
	prometheus.MustRegister(appliedMessages)
}

type robustSnapshot struct {
	firstIndex uint64
	lastIndex  uint64
	store      *raft_store.LevelDBStore
	del        map[uint64]bool
	parsed     map[uint64]*types.RobustMessage
	sliced     map[int64][]*types.RobustMessage
	servers    map[int64]bool
}

func (s *robustSnapshot) canCompact(session types.RobustId, msg *types.RobustMessage, logIndex uint64) (bool, int, error) {
	slicedIdx := -1
	// TODO: Instead of doing a full scan, we should be able to use binary
	// search here since the message IDs are strictly monotonically increasing
	// and appended in-order, hence sorted.
	for idx, smsg := range s.sliced[session.Id] {
		if smsg != nil && smsg.Id == msg.Id {
			slicedIdx = idx
		}
	}
	if slicedIdx == -1 && msg.Type != types.RobustConfig && msg.Type != types.RobustMessageOfDeath {
		log.Printf("WARNING: message with id %v (logidx %d), type %s for session %v (data = %s) not found in s.sliced, using slow path\n", msg.Id, logIndex, msg.Type, msg.Session, msg.Data)
		p := irc.ParseMessage(msg.Data)
		if p != nil {
			log.Printf(" (parsed: %s)\n", p.Bytes())
		}
	}

	// TODO: deprecate get, prevSlow, nextSlow once there have been no fallbacks to the slow path for a week.

	// The prev and next functions are cursors, see ircserver’s StillRelevant
	// function. They return the previous message (or next message,
	// respectively).
	get := func(index uint64, wantType types.RobustType) *types.RobustMessage {
		if s.del[index] {
			return nil
		}

		nmsg, ok := s.parsed[index]
		if !ok {
			return nil
		}

		if wantType != types.RobustAny && wantType != nmsg.Type {
			return nil
		}

		if nmsg.Session != session &&
			(nmsg.Type != types.RobustCreateSession ||
				nmsg.Id != session) {
			return nil
		}
		return nmsg
	}

	nextIndex := logIndex
	nextIndexSliced := slicedIdx
	trace := false

	reset := func() {
		nextIndex = logIndex
		nextIndexSliced = slicedIdx
	}

	prevSlow := func(wantType types.RobustType) (*types.RobustMessage, error) {
		for {
			nextIndex--

			if nextIndex < s.firstIndex {
				return nil, ircserver.CursorEOF
			}

			if ircmsg := get(nextIndex, wantType); ircmsg != nil {
				if trace {
					log.Printf("Returning idx=%d: %v\n", nextIndex, ircmsg.Data)
				}
				return ircmsg, nil
			}
		}
	}

	prevFast := func(wantType types.RobustType) (*types.RobustMessage, error) {
		if nextIndexSliced == -1 {
			glog.Errorf("Compaction: falling back to prevSlow() for log index %d, msg id %v\n", logIndex, msg.Id)
			return prevSlow(wantType)
		}
		for {
			nextIndexSliced--
			if nextIndexSliced < 0 {
				return nil, ircserver.CursorEOF
			}
			nmsg := s.sliced[session.Id][nextIndexSliced]

			if nmsg == nil {
				continue
			}

			// TODO: introduce a parameter so that the relevant* functions
			// can specify whether they want just messages from _the same_
			// session or messages that are relevant to the session.
			if nmsg.Session != session &&
				(nmsg.Type != types.RobustCreateSession ||
					nmsg.Id != session) {
				continue
			}

			if wantType == types.RobustAny || wantType == nmsg.Type {
				if trace {
					log.Printf("Returning sliceidx=%d: %v\n", nextIndexSliced, nmsg.Data)
				}
				return nmsg, nil
			}
		}
	}

	nextSlow := func(wantType types.RobustType) (*types.RobustMessage, error) {
		for {
			nextIndex++

			if nextIndex > s.lastIndex {
				return nil, ircserver.CursorEOF
			}

			if ircmsg := get(nextIndex, wantType); ircmsg != nil {
				if trace {
					log.Printf("Returning idx=%d: %v\n", nextIndex, ircmsg.Data)
				}
				return ircmsg, nil
			}
		}
	}

	nextFast := func(wantType types.RobustType) (*types.RobustMessage, error) {
		if nextIndexSliced == -1 {
			glog.Errorf("Compaction: falling back to nextSlow() for log index %d, msg id %v\n", logIndex, msg.Id)
			return nextSlow(wantType)
		}

		for {
			nextIndexSliced++
			if nextIndexSliced >= len(s.sliced[session.Id]) {
				return nil, ircserver.CursorEOF
			}
			nmsg := s.sliced[session.Id][nextIndexSliced]

			if nmsg == nil {
				continue
			}

			// TODO: introduce a parameter so that the relevant* functions
			// can specify whether they want just messages from _the same_
			// session or messages that are relevant to the session.
			if nmsg.Session != session &&
				(nmsg.Type != types.RobustCreateSession ||
					nmsg.Id != session) {
				continue
			}

			if wantType == types.RobustAny || wantType == nmsg.Type {
				if trace {
					log.Printf("Returning sliceidx=%d: %v\n", nextIndexSliced, nmsg.Data)
				}
				return nmsg, nil
			}
		}
	}

	switch msg.Type {
	case types.RobustDeleteSession:
		rmsg, err := prevSlow(types.RobustAny)
		if err != nil && err != ircserver.CursorEOF {
			return false, slicedIdx, err
		}
		return err == nil && rmsg.Type == types.RobustCreateSession, slicedIdx, nil

	case types.RobustCreateSession:
		_, err := nextSlow(types.RobustAny)
		if err != nil && err != ircserver.CursorEOF {
			return false, slicedIdx, err
		}

		// Sessions can be compacted away when they don’t contain any messages.
		return err == ircserver.CursorEOF, slicedIdx, nil

	case types.RobustIRCFromClient:
		p := irc.ParseMessage(msg.Data)

		relevantFast, errFast := ircServer.StillRelevant(s.servers[session.Id], p, prevFast, nextFast, reset)
		return !relevantFast, slicedIdx, errFast

	case types.RobustMessageOfDeath:
		// Messages of death can always be compacted, they will be skipped anyway.
		return true, slicedIdx, nil

	case types.RobustConfig:
		// TODO: implement compaction for RobustConfig
		return false, slicedIdx, nil

	case types.RobustIRCToClient:
		fallthrough
	case types.RobustPing:
		fallthrough
	case types.RobustAny:
		glog.Errorf("Compaction: saw unexpected message type %v (log id %d, message id %v)\n", msg.Type, logIndex, msg.Id)
		return false, slicedIdx, nil

	default:
		glog.Errorf("Compaction: saw message of unknown type %d (log id %d, message id %v)\n", msg.Type, logIndex, msg.Id)
		return false, slicedIdx, nil
	}
}

func (s *robustSnapshot) Persist(sink raft.SnapshotSink) error {
	log.Printf("Filtering and writing %d indexes\n", s.lastIndex-s.firstIndex)

	// Get a timestamp and keep it constant, so that we only compact messages
	// older than n days from compactionStart. If we used time.Since, new
	// messages would pour into the window on every compaction round, possibly
	// making the compaction never converge.
	compactionStart := time.Now()

	sessions := make(map[types.RobustId]bool)

	// First pass: just parse all the messages
	for i := s.firstIndex; i <= s.lastIndex; i++ {
		var nlog raft.Log
		if err := s.store.GetLog(i, &nlog); err != nil {
			s.del[i] = true
			continue
		}

		// TODO: compact raft messages as well, so that peer changes are not kept forever
		if nlog.Type != raft.LogCommand {
			continue
		}

		parsed := types.NewRobustMessageFromBytes(nlog.Data)
		s.parsed[i] = &parsed

		if parsed.Type == types.RobustCreateSession {
			s.sliced[parsed.Id.Id] = append(s.sliced[parsed.Id.Id], &parsed)
		}

		if parsed.Type == types.RobustDeleteSession {
			s.sliced[parsed.Session.Id] = append(s.sliced[parsed.Session.Id], &parsed)
		}

		if parsed.Type == types.RobustIRCFromClient {
			// TODO: skip PING/PRIVMSG messages that are outside of the
			// compaction window to reduce the working set. should be a noop
			// since no relevant* function looks at PRIVMSG/PING.
			sessions[parsed.Session] = true
			vmsgs, _ := ircServer.Get(types.RobustId{Id: parsed.Id.Id})

			onlyerrors := true
			for _, msg := range vmsgs {
				if msg.Type != types.RobustIRCToClient {
					glog.Errorf("Unexpected output message type %v\n", msg.Type)
					continue
				}
				ircmsg := irc.ParseMessage(msg.Data)
				if ircmsg == nil {
					glog.Errorf("Output message not parsable\n")
					continue
				}
				if !errorCodes[ircmsg.Command] {
					onlyerrors = false
				}
			}
			if len(vmsgs) > 0 && onlyerrors {
				s.del[i] = true
				continue
			}

			// Kind of a hack: we need to keep track of which sessions are
			// services connections and which are not, so that we can look at
			// the correct relevant-function (e.g. server_NICK vs. NICK).
			ircmsg := irc.ParseMessage(parsed.Data)
			if ircmsg != nil && strings.ToUpper(ircmsg.Command) == "SERVER" {
				s.servers[parsed.Session.Id] = true
			}

			// Every session which is interested in at least one of the output
			// messages gets a pointer to the input message stored in s.sliced
			// so that we can easily iterate over all relevant input messages.
			for session := range sessions {
				interested := false
				for _, msg := range vmsgs {
					if msg.InterestingFor[session.Id] {
						interested = true
						break
					}
				}
				// All messages that would be delivered to a session are
				// interesting for compaction, but also just any message that a
				// specific session sent.
				if interested || (len(vmsgs) > 0 && session.Id == parsed.Session.Id) {
					s.sliced[session.Id] = append(s.sliced[session.Id], &parsed)
				}
			}

			// Some messages don’t result in output messages, such as a JOIN
			// command for a channel the user is already in. We mark these as
			// interesting at least to the session from which they originated,
			// so that they can be detected and deleted.
			// TODO: would it be okay to just mark them for deletion? i.e., do all messages that modify state return a result?
			if len(vmsgs) == 0 {
				s.sliced[parsed.Session.Id] = append(s.sliced[parsed.Session.Id], &parsed)
			}
		}
	}

	log.Printf("got %d sessions\n", len(sessions))
	for session := range sessions {
		log.Printf("session 0x%x has %d messages\n", session.Id, len(s.sliced[session.Id]))
	}

	// We repeatedly compact, since the result of one compaction can affect the
	// result of other compactions (see compaction_test.go for examples).
	changed := true
	pass := 0
	for changed {
		log.Printf("Compaction pass %d\n", pass)
		pass++
		changed = false
		for i := s.firstIndex; i <= s.lastIndex; i++ {
			if i%1000 == 0 {
				log.Printf("message %d of %d (%.0f%%)\n",
					i, s.lastIndex, (float64(i)/float64(s.lastIndex))*100.0)
			}
			if s.del[i] {
				continue
			}

			msg, ok := s.parsed[i]
			if !ok {
				continue
			}

			if compactionStart.Sub(time.Unix(0, msg.Id.Id)) < 7*24*time.Hour {
				// If we ran outside the window, we don’t even need to look at
				// any newer messages anymore.
				break
			}

			session := msg.Session
			if msg.Type == types.RobustCreateSession {
				session = msg.Id
			}

			canCompact, slicedIdx, err := s.canCompact(session, msg, i)
			if err != nil {
				sink.Cancel()
				return err
			}
			if canCompact {
				s.del[i] = true
				if slicedIdx != -1 {
					s.sliced[session.Id][slicedIdx] = nil
				}
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
			continue
		}

		if err := encoder.Encode(elog); err != nil {
			sink.Cancel()
			return err
		}
	}

	sink.Close()

	for idx, del := range s.del {
		if !del {
			continue
		}
		nmsg, ok := s.parsed[idx]
		// If the message was not found in parsed, then there was no message
		// with this index, hence there is nothing to delete.
		if !ok {
			continue
		}
		ircServer.Delete(nmsg.Id)
		s.store.DeleteRange(idx, idx)
	}

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
	log.Printf("Apply(msg.Type=%s)\n", msg.Type)

	defer func() {
		if msg.Type == types.RobustMessageOfDeath {
			return
		}
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
				glog.Fatalf("Could not marshal message: %v", err)
			}
			l.Data = data
			if err := fsm.store.StoreLog(l); err != nil {
				glog.Fatalf("Could not store log while marking message as message of death: %v", err)
			}
			log.Printf("Marked %+v as message of death\n", l)
			glog.Fatalf("%v", r)
		}
	}()

	switch msg.Type {
	case types.RobustMessageOfDeath:
		// To prevent the message from being accepted again.
		ircServer.UpdateLastClientMessageID(&msg, l.Data)
		log.Printf("Skipped message of death.\n")

	case types.RobustCreateSession:
		ircServer.CreateSession(msg.Id, msg.Data)

	case types.RobustDeleteSession:
		if _, err := ircServer.GetSession(msg.Session); err == nil {
			// TODO(secure): overwrite QUIT messages for services with an faq entry explaining that they are not robust yet.
			reply := ircServer.ProcessMessage(msg.Id, msg.Session, irc.ParseMessage("QUIT :"+string(msg.Data)))
			ircServer.SendMessages(reply, msg.Session, msg.Id.Id)
		}

	case types.RobustIRCFromClient:
		// Need to do this first, because ircserver.ProcessMessage could delete
		// the session, e.g. by using KILL or QUIT.
		if err := ircServer.UpdateLastClientMessageID(&msg, l.Data); err != nil {
			log.Printf("Error updating the last message for session: %v\n", err)
		} else {
			reply := ircServer.ProcessMessage(msg.Id, msg.Session, irc.ParseMessage(string(msg.Data)))
			ircServer.SendMessages(reply, msg.Session, msg.Id.Id)
		}

	case types.RobustConfig:
		newCfg, err := config.FromString(string(msg.Data))
		if err != nil {
			log.Printf("Skipping unexpectedly invalid configuration (%v)\n", err)
		} else {
			netConfig = newCfg
			ircServer.Config = netConfig.IRC
		}
	}

	appliedMessages.WithLabelValues(msg.Type.String()).Inc()

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
		firstIndex: first,
		lastIndex:  last,
		store:      fsm.ircstore,
		del:        make(map[uint64]bool),
		parsed:     make(map[uint64]*types.RobustMessage),
		sliced:     make(map[int64][]*types.RobustMessage),
		servers:    make(map[int64]bool),
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

	ircServer = ircserver.NewIRCServer(*network, time.Now())

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
		printDefault(flag.Lookup("canary_report"))
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

	if *canaryReport != "" {
		canary()
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

	if *peerAddr == "" {
		log.Printf("-peer_addr not set, initializing to %q. Make sure %q is a host:port string that other raft nodes can connect to!\n", *listen, *listen)
		*peerAddr = *listen
	}

	ircServer = ircserver.NewIRCServer(*network, time.Now())

	transport := rafthttp.NewHTTPTransport(
		*peerAddr,
		// Not deadlined, otherwise snapshot installments fail.
		robusthttp.Client(*networkPassword, false),
		nil,
		"")

	peerStore = raft.NewJSONPeers(*raftDir, transport)

	var p []string

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
	config.LeaderLeaseTimeout = 2 * time.Second
	config.HeartbeatTimeout = config.LeaderLeaseTimeout
	config.ElectionTimeout = config.LeaderLeaseTimeout

	// We use prometheus, so hook up the metrics package (used by raft) to
	// prometheus as well.
	sink, err := metrics_prometheus.NewPrometheusSink()
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

	node, err = raft.NewRaft(config, fsm, logcache, logStore, fss, peerStore, transport)
	if err != nil {
		log.Fatal(err)
	}

	privaterouter := httprouter.New()
	privaterouter.Handler("GET", "/", exitOnRecoverHandleFunc(handleStatus))
	privaterouter.Handler("GET", "/irclog", exitOnRecoverHandleFunc(handleIrclog))
	privaterouter.Handler("POST", "/raft/*rest", exitOnRecoverHandler(transport))
	privaterouter.Handler("POST", "/join", exitOnRecoverHandleFunc(handleJoin))
	privaterouter.Handler("GET", "/snapshot", exitOnRecoverHandleFunc(handleSnapshot))
	privaterouter.Handler("GET", "/leader", exitOnRecoverHandleFunc(handleLeader))
	privaterouter.Handler("GET", "/canarylog", exitOnRecoverHandleFunc(handleCanaryLog))
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

	expireSessionsTimer := time.After(expireSessionsInterval)
	for {
		select {
		case <-expireSessionsTimer:
			expireSessionsTimer = time.After(expireSessionsInterval)

			// Race conditions (a node becoming a leader or ceasing to be the
			// leader shortly before/after this runs) are okay, since the timer
			// is triggered often enough on every node so that it will
			// eventually run on the leader.
			if node.State() != raft.Leader {
				continue
			}

			applyMu.Lock()
			for _, msg := range ircServer.ExpireSessions(time.Duration(netConfig.SessionExpiration)) {
				// Cannot fail, no user input.
				msgbytes, _ := json.Marshal(msg)
				f := node.Apply(msgbytes, 10*time.Second)
				if err := f.Error(); err != nil {
					log.Printf("Apply(): %v\n", err)
					break
				}
			}
			applyMu.Unlock()
		}
	}
}
