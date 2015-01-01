package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// trivial log store, writing one entry into one file each.
// fulfills the raft.LogStore interface.
type fancyLogStore struct {
	l         sync.RWMutex
	lowIndex  uint64
	highIndex uint64
}

type uint64Slice []uint64

func (p uint64Slice) Len() int           { return len(p) }
func (p uint64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p uint64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// GetAll returns all indexes that are currently present in the log store. This
// is NOT part of the raft.LogStore interface — we use it when snapshotting.
func (s *fancyLogStore) GetAll() ([]uint64, error) {
	var indexes []uint64
	dir, err := os.Open(filepath.Join(*raftDir, "fancylogs"))
	if err != nil {
		return indexes, err
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return indexes, err
	}

	for _, name := range names {
		if !strings.HasPrefix(name, "entry.") {
			continue
		}

		dot := strings.LastIndex(name, ".")
		if dot == -1 {
			continue
		}

		index, err := strconv.ParseInt(name[dot+1:], 0, 64)
		if err != nil {
			return indexes, fmt.Errorf("Unexpected filename, does not confirm to entry.%%d: %q. Parse error: %v", name, err)
		}

		indexes = append(indexes, uint64(index))
	}

	sort.Sort(uint64Slice(indexes))

	return indexes, nil
}

func (s *fancyLogStore) FirstIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.lowIndex, nil
}

func (s *fancyLogStore) LastIndex() (uint64, error) {
	s.l.RLock()
	defer s.l.RUnlock()
	return s.highIndex, nil
}

func (s *fancyLogStore) GetLog(index uint64, rlog *raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()
	f, err := os.Open(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", index)))
	if err != nil {
		return err
	}
	defer f.Close()

	var elog raft.Log
	if err := gob.NewDecoder(f).Decode(&elog); err != nil {
		return err
	}
	*rlog = elog
	return nil
}

func (s *fancyLogStore) StoreLog(log *raft.Log) error {
	return s.StoreLogs([]*raft.Log{log})
}

func (s *fancyLogStore) StoreLogs(logs []*raft.Log) error {
	s.l.Lock()
	defer s.l.Unlock()

	for _, entry := range logs {
		log.Printf("writing index %d to file (%v)\n", entry.Index, entry)
		f, err := os.Create(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", entry.Index)))
		if err != nil {
			return err
		}
		defer f.Close()
		if err := gob.NewEncoder(f).Encode(entry); err != nil {
			return err
		}
		if entry.Index < s.lowIndex || s.lowIndex == 0 {
			s.lowIndex = entry.Index
		}
		if entry.Index > s.highIndex {
			s.highIndex = entry.Index
		}
	}

	return nil
}

func (s *fancyLogStore) DeleteRange(min, max uint64) error {
	s.l.Lock()
	defer s.l.Unlock()
	for i := min; i <= max; i++ {
		log.Printf("deleting index %d\n", i)
		if err := os.Remove(filepath.Join(*raftDir, fmt.Sprintf("fancylogs/entry.%d", i))); err != nil {
			return err
		}
	}
	return nil
}

type fancyStableStore struct {
}

func (s *fancyStableStore) Set(key []byte, val []byte) error {
	return ioutil.WriteFile(filepath.Join(*raftDir, "fancystable", string(key)), val, 0600)
}

func (s *fancyStableStore) Get(key []byte) ([]byte, error) {
	b, err := ioutil.ReadFile(filepath.Join(*raftDir, "fancystable", string(key)))
	if err != nil && os.IsNotExist(err) {
		return []byte{}, fmt.Errorf("not found")
	}
	return b, err
}

func (s *fancyStableStore) SetUint64(key []byte, val uint64) error {
	return s.Set(key, []byte(fmt.Sprintf("%d", val)))
}

func (s *fancyStableStore) GetUint64(key []byte) (uint64, error) {
	b, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	i, err := strconv.ParseInt(string(b), 0, 64)
	if err != nil {
		return 0, err
	}
	return uint64(i), nil
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

func redirectToLeader(w http.ResponseWriter, r *http.Request) {
	leader := node.Leader()
	if leader == nil {
		http.Error(w, fmt.Sprintf("No leader known. Please try another server."),
			http.StatusInternalServerError)
		return
	}

	target := r.URL
	target.Scheme = "http"
	target.Host = leader.String()
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func sessionForRequest(r *http.Request) (types.FancyId, error) {
	idstr := mux.Vars(r)["sessionid"]
	id, err := strconv.ParseInt(idstr, 0, 64)
	if err != nil {
		return types.FancyId(0), fmt.Errorf("Invalid session: %v", err)
	}

	return types.FancyId(id), nil
}

// handlePostMessage is called by the fancyproxy whenever a message should be
// posted. The handler blocks until either the data was written or an error
// occurred. If successful, it returns the unique id of the message.
func handlePostMessage(w http.ResponseWriter, r *http.Request) {
	session, err := sessionForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// TODO(secure): read at most 512 byte of body, as the IRC RFC restricts
	// messages to be that length. this also protects us from “let’s send a
	// large body” attacks.
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request:", err)
		return
	}

	// TODO(secure): properly check that we can convert data to a string at all.
	msg := types.NewFancyMessage(types.FancyIRCFromClient, session, string(data))
	msgbytes, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, fmt.Sprintf("Could not store message, cannot encode it as JSON: %v", err),
			http.StatusInternalServerError)
		return
	}

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			redirectToLeader(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}

	if replies, ok := f.Response().([]irc.Message); ok {
		for _, msg := range replies {
			fancymsg := types.NewFancyMessage(types.FancyIRCToClient, session, string(msg.Bytes()))
			// TODO(secure): error handling
			fancymsgbytes, _ := json.Marshal(fancymsg)
			log.Printf("[DEBUG] apply %+v (-> %+v)\n", msg, fancymsg)
			// TODO(secure): error handling
			node.Apply(fancymsgbytes, 10*time.Second)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(msgbytes)
}

func handleJoin(res http.ResponseWriter, req *http.Request) {
	log.Println("Join request from", req.RemoteAddr)
	if node.State() != raft.Leader {
		log.Println("rejecting join request, I am not the leader")
		http.Redirect(res, req, fmt.Sprintf("http://%s/join", node.Leader()), 307)
		return
	}

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Println("Could not read body:", err)
		return
	}

	type joinRequest struct {
		Addr string
	}
	var r joinRequest

	if err = json.Unmarshal(data, &r); err != nil {
		log.Println("Could not decode request:", err)
		http.Error(res, fmt.Sprintf("Could not decode your request"), 400)
		return
	}

	log.Printf("Joining peer: %q\n", r.Addr)

	a, err := net.ResolveTCPAddr("tcp", r.Addr)
	if err != nil {
		log.Printf("Could not resolve addr %q: %v\n", r.Addr, err)
		http.Error(res, "Could not resolve your address", 400)
		return
	}

	if err = node.AddPeer(a).Error(); err != nil {
		log.Println("Could not add peer:", err)
		http.Error(res, "Could not add peer", 500)
		return
	}
	res.WriteHeader(200)
}

var statusTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
	<head>
		<title>Status of fancyirc node {{ .Addr }}</title>
		<style>
			th {
				text-align: left;
				padding: 0.2em;
			}
		</style>
	</head>
	<body>
		<h1>Status of fancyirc node {{ .Addr }}</h1>
		<table>
			<tr>
				<th>State</th>
				<td>{{ .State }}</dd>
			</tr>
			<tr>
				<th>Current Leader</th>
				<td>{{ .Leader }}</td>
			</tr>
			<tr>
				<th>Peers</th>
				<td>{{ .Peers }}</td>
			</tr>
		</table>
		<h2>Log</h2>
		<p>Entries {{ .First }} through {{ .Last }}</p>
		<ul>
		{{ range .Entries }}
			<li>idx={{ .Index }} term={{ .Term }} type={{ .Type }} data={{ .Data }}<br><code>str={{ .Data | printf "%s"}}</code></li>
		{{ end }}
		</ul>
		<h2>Stats</h2>
		<table>
		{{ range $key, $val := .Stats }}
			<tr>
				<th>{{ $key }}</th>
				<td>{{ $val }}</td>
			</tr>
		{{ end }}
		</table>
	</body>
</html>`))

func handleStatus(res http.ResponseWriter, req *http.Request) {
	p, _ := peerStore.Peers()

	lo, err := logStore.FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}
	hi, err := logStore.LastIndex()
	if err != nil {
		log.Printf("Could not get last index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}

	var entries []*raft.Log
	if lo != 0 && hi != 0 {
		for i := lo; i <= hi; i++ {
			l := new(raft.Log)

			if err := logStore.GetLog(i, l); err != nil {
				log.Printf("Could not get entry %d: %v", i, err)
				http.Error(res, "internal error", 500)
				return
			}
			entries = append(entries, l)
		}
	}

	args := struct {
		Addr    string
		State   raft.RaftState
		Leader  net.Addr
		Peers   []net.Addr
		First   uint64
		Last    uint64
		Entries []*raft.Log
		Stats   map[string]string
	}{
		*listen,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
	}

	statusTpl.Execute(res, args)
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

func handleSnapshot(res http.ResponseWriter, req *http.Request) {
	log.Printf("snapshotting()\n")
	node.Snapshot()
	log.Println("snapshotted")
}

func handleGetMessages(w http.ResponseWriter, r *http.Request) {
	session, err := sessionForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// TODO: read params
	// TODO: register in stats that we are sending

	if r.FormValue("lastseen") != "" {
		lastSeen, err := strconv.ParseInt(r.FormValue("lastseen"), 0, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid value for lastseen: %v", err),
				http.StatusInternalServerError)
			return
		}

		// TODO(secure): don’t send everything, skip everything until the last seen message
		log.Printf("need to skip to %d\n", lastSeen)
	}

	// fancyLogStore’s FirstIndex() and LastIndex() cannot fail.
	idx, _ := logStore.FirstIndex()
	for {
		newEvent.L.Lock()
		last, _ := logStore.LastIndex()
		if idx > last {
			// Sleep until FSM.Apply() wakes us up for a new message.
			newEvent.Wait()
			newEvent.L.Unlock()
			continue
		}
		newEvent.L.Unlock()

		var elog raft.Log
		if err := logStore.GetLog(idx, &elog); err != nil {
			log.Fatalf("GetLog(%d) failed (LastIndex() = %d): %v\n", idx, last, err)
		}

		idx++

		if elog.Type != raft.LogCommand {
			continue
		}

		msg := types.NewFancyMessageFromBytes(elog.Data)
		if msg.Type != types.FancyIRCToClient || !sessions[session].interestedIn(msg) {
			continue
		}

		if _, err := w.Write(elog.Data); err != nil {
			log.Printf("Error encoding JSON: %v\n", err)
			return
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	msg := types.NewFancyMessage(types.FancyCreateSession, types.FancyId(0), "")
	// Cannot fail, no user input.
	msgbytes, _ := json.Marshal(msg)

	f := node.Apply(msgbytes, 10*time.Second)
	if err := f.Error(); err != nil {
		if err == raft.ErrNotLeader {
			redirectToLeader(w, r)
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}

	sessionid := fmt.Sprintf("0x%x", msg.Id)

	w.Header().Set("Content-Type", "application/json")

	type createSessionReply struct {
		Sessionid string
		Prefix    string
	}

	if err := json.NewEncoder(w).Encode(createSessionReply{sessionid, *network}); err != nil {
		log.Printf("Could not send /session reply: %v\n", err)
	}
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionid := mux.Vars(r)["sessionid"]
	log.Printf("TODO: delete session %q\n", sessionid)
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
