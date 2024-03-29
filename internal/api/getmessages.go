package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/ircserver"
	"github.com/robustirc/robustirc/internal/outputstream"
	"github.com/robustirc/robustirc/internal/robust"
	"github.com/stapelberg/glog"
)

func (api *HTTP) pingMessage() *robust.Message {
	cfgf := api.raftNode.GetConfiguration()
	if err := cfgf.Error(); err != nil {
		log.Fatalf("Could not get peers: %v", err)
	}
	servers := cfgf.Configuration().Servers
	peers := make([]string, len(servers))
	for idx, server := range servers {
		peers[idx] = string(server.Address)
	}
	return &robust.Message{
		Type:    robust.Ping,
		Servers: peers,
	}
}

func parseLastSeen(lastSeenStr string) (first uint64, last uint64, err error) {
	parts := strings.Split(lastSeenStr, ".")
	if got, want := len(parts), 2; got != want {
		return 0, 0, fmt.Errorf("Unexpected number of parts: got %d, want %d", got, want)
	}
	first, err = strconv.ParseUint(parts[0], 0, 64)
	if err != nil {
		return 0, 0, err
	}
	last, err = strconv.ParseUint(parts[1], 0, 64)
	if err != nil {
		return 0, 0, err
	}
	return first, last, nil
}

func (api *HTTP) pingTicker(ctx context.Context, msgschan chan<- []*robust.Message) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			msgschan <- []*robust.Message{api.pingMessage()}
		case <-ctx.Done():
			return
		}
	}
}

func outputToRobustMessages(msgs []outputstream.Message) []*robust.Message {
	result := make([]*robust.Message, len(msgs))
	for idx, msg := range msgs {
		result[idx] = &robust.Message{
			Id:             msg.Id,
			Type:           robust.IRCToClient,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		}
	}
	return result
}

func (api *HTTP) getMessages(ctx context.Context, lastSeen robust.Id, msgschan chan<- []*robust.Message) {
	var msgs []outputstream.Message

	// With the following output messages stored for a session:
	// Id                  Reply
	// 1431542836610113945.1
	// 1431542836610113945.2     |lastSeen|
	// 1431542836610113945.3
	// 1431542836691955391.1
	// …when resuming, GetNext(1431542836610113945.2) will return
	// 1431542836691955391.*, skipping the remaining messages with
	// Id=1431542836610113945.
	// Hence, we need to Get(1431542836610113945.2) to send
	// 1431542836610113945.3 and following to the client.
	if msgs, ok := api.output().Get(lastSeen); ok && int(lastSeen.Reply) < len(msgs) {
		select {
		case <-ctx.Done():
			return
		case msgschan <- outputToRobustMessages(msgs[lastSeen.Reply:]):
		}
	}

	for {
		if msgs = api.output().GetNext(ctx, lastSeen); len(msgs) == 0 {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		// This check prevents replaying old messages in the scenario where
		// the client has seen newer messages than the server, for example
		// because the server is currently recovering from a snapshot after
		// being restarted, or because the server’s network connection to
		// the rest of the network is currently slow.
		if msgs[0].Id.Id < lastSeen.Id {
			glog.Warningf("lastSeen (%d) more recent than GetNext() result %d\n", lastSeen.Id, msgs[0].Id.Id)
			glog.Warningf("This should only happen while the server is recovering from a snapshot\n")
			glog.Warningf("The message in question is %v\n", msgs[0])
			// Prevent busylooping while new messages are applied.
			time.Sleep(250 * time.Millisecond)
			continue
		}

		lastSeen = msgs[0].Id
		select {
		case <-ctx.Done():
			return
		case msgschan <- outputToRobustMessages(msgs):
		}
	}
}

// partitioned reports whether this node is neither the leader nor was it
// recently in contact with the leader, suggesting that it is partitioned from
// the rest of the network.
func (api *HTTP) partitioned() (time.Time, bool) {
	state := api.raftNode.State()
	if state == raft.Leader {
		return time.Time{}, false
	}

	if state == raft.Follower {
		// The 10 seconds threshold is arbitrary. The only criterion is
		// that it must be higher than raft’s HeartbeatTimeout of 2s. The
		// higher it is chosen, the longer users have to wait until they
		// can connect to a different node.
		lastContact := api.raftNode.LastContact()
		return lastContact, time.Since(lastContact) > 10*time.Second
	}

	// Neither leader nor follower, maybe still trying to join the network after
	// restarting?
	return time.Time{}, true
}

func (api *HTTP) handleGetMessages(w http.ResponseWriter, r *http.Request, sessionId string) {
	// Avoid sessionOrProxy() because GetMessages can be answered on any raft
	// node, it’s a read-only request.
	session, err := api.session(r, sessionId)
	if err != nil {
		if err == ircserver.ErrSessionNotYetSeen {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	lastSeen := session
	if ls := r.FormValue("lastseen"); ls != "0.0" && ls != "" {
		first, last, err := parseLastSeen(ls)
		if err != nil {
			log.Printf("cannot parse %q: %v\n", ls, err)
			http.Error(w, fmt.Sprintf("Malformed lastseen value (%q)", ls),
				http.StatusInternalServerError)
			return
		}
		lastSeen = robust.Id{
			Id:    first,
			Reply: last,
		}
		log.Printf("Trying to resume at %v\n", lastSeen)
	}

	// Fail early if raft is not functional right now:
	if lastContact, partitioned := api.partitioned(); partitioned {
		// Reject this GetMessages request so that clients can connect to a
		// different server and receive new messages.
		log.Printf("Rejecting GetMessages request due to LastContact (%v) too long ago\n", lastContact)
		http.Error(w, fmt.Sprintf("raft: LastContact (%v) too long ago", lastContact), http.StatusInternalServerError)
		return
	}

	// Acknowledge the request even if no messages need to be sent right now:
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	enc := json.NewEncoder(w)
	flushTimer := time.NewTimer(1 * time.Second)
	flushTimer.Stop()
	var lastFlush time.Time
	willFlush := false
	msgschan := make(chan []*robust.Message)

	sessionId = strconv.FormatUint(session.Id, 10)
	ctx, cancel := context.WithCancel(r.Context())
	// Cancel the helper goroutines we are about to start when this
	// request handler returns.
	wasSuperseded := false
	cancelAll := func(superseded bool) {
		if superseded {
			// cancelAll will be called after the context cancellation
			// via defer, so save that this GetMessages request was
			// superseded.
			wasSuperseded = true
		}
		cancel()
		// Wake up the getMessages goroutine from its GetNext call
		// to quickly free up resources.
		api.output().InterruptGetNext()

		if !wasSuperseded {
			api.deleteGetMessagesRequests(sessionId)
		}
	}

	api.setGetMessagesRequests(sessionId, GetMessagesStats{
		RemoteAddr:    r.RemoteAddr,
		Session:       session,
		Nick:          api.ircServer().GetNick(session),
		Started:       time.Now(),
		UserAgent:     r.Header.Get("User-Agent"),
		ForwardedFor:  r.Header.Get("X-Forwarded-For"),
		TrustedBridge: api.ircServer().TrustedBridge(r.Header.Get("X-Bridge-Auth")),
		cancel:        cancelAll,
		api:           api,
	})

	defer cancelAll(false)

	go api.pingTicker(ctx, msgschan)
	go api.getMessages(ctx, lastSeen, msgschan)

	for {
		select {
		case <-ctx.Done():
			return

		case msgs := <-msgschan:
			for _, msg := range msgs {
				if msg.Type != robust.Ping && !msg.InterestingFor[session.Id] {
					continue
				}

				if err := enc.Encode(msg); err != nil {
					log.Printf("Error encoding JSON: %v\n", err)
					return
				}
			}

			if _, err := api.ircServer().GetSession(session); err != nil {
				// Session was deleted in the meanwhile, abort this request.
				return
			}

			if lastContact, partitioned := api.partitioned(); partitioned {
				// We abort this GetMessages request so that clients can connect
				// to a different server and receive new messages.
				log.Printf("Aborting GetMessages request due to LastContact (%v) too long ago\n", lastContact)
				return
			}

			if time.Since(lastFlush) > 100*time.Millisecond {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				lastFlush = time.Now()
			} else if !willFlush {
				// Delay flushing by 10ms to avoid flushing too often, which
				// results in lots of write() syscall.
				flushTimer.Reset(10 * time.Millisecond)
				willFlush = true
			}

		case <-flushTimer.C:
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			willFlush = false
			lastFlush = time.Now()
		}
	}
}
