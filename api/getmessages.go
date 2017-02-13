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
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/outputstream"
	"github.com/robustirc/robustirc/types"
	"github.com/stapelberg/glog"
)

func (api *HTTP) updateLastContact() {
	r := api.raftNode
	// node.LastContact() is only updated when we receive heartbeats from the
	// leader, i.e. only when we are a follower.
	if r.State() == raft.Follower && !r.LastContact().IsZero() {
		lastContact = r.LastContact()
	} else if r.State() == raft.Leader {
		lastContact = time.Now()
	}
}

func (api *HTTP) pingMessage() *types.RobustMessage {
	peers, err := api.peerStore.Peers()
	if err != nil {
		log.Fatalf("Could not get peers: %v (Peer file corrupted on disk?)", err)
	}
	return &types.RobustMessage{
		Type:    types.RobustPing,
		Servers: peers,
	}
}

func parseLastSeen(lastSeenStr string) (first int64, last int64, err error) {
	parts := strings.Split(lastSeenStr, ".")
	if got, want := len(parts), 2; got != want {
		return 0, 0, fmt.Errorf("Unexpected number of parts: got %d, want %d", got, want)
	}
	first, err = strconv.ParseInt(parts[0], 0, 64)
	if err != nil {
		return 0, 0, err
	}
	last, err = strconv.ParseInt(parts[1], 0, 64)
	if err != nil {
		return 0, 0, err
	}
	return first, last, nil
}

func (api *HTTP) pingTicker(ctx context.Context, msgschan chan<- []*types.RobustMessage) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			msgschan <- []*types.RobustMessage{api.pingMessage()}
		case <-ctx.Done():
			return
		}
	}
}

func outputToRobustMessages(msgs []outputstream.Message) []*types.RobustMessage {
	result := make([]*types.RobustMessage, len(msgs))
	for idx, msg := range msgs {
		result[idx] = &types.RobustMessage{
			Id:             msg.Id,
			Type:           types.RobustIRCToClient,
			Data:           msg.Data,
			InterestingFor: msg.InterestingFor,
		}
	}
	return result
}

func (api *HTTP) getMessages(ctx context.Context, lastSeen types.RobustId, msgschan chan<- []*types.RobustMessage) {
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
	if msgs, ok := api.output.Get(lastSeen); ok && int(lastSeen.Reply) < len(msgs) {
		select {
		case <-ctx.Done():
			return
		case msgschan <- outputToRobustMessages(msgs[lastSeen.Reply:]):
		}
	}

	for {
		if msgs = api.output.GetNext(ctx, lastSeen); len(msgs) == 0 {
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
		lastSeen = types.RobustId{
			Id:    first,
			Reply: last,
		}
		log.Printf("Trying to resume at %v\n", lastSeen)
	}

	enc := json.NewEncoder(w)
	flushTimer := time.NewTimer(1 * time.Second)
	flushTimer.Stop()
	var lastFlush time.Time
	willFlush := false
	msgschan := make(chan []*types.RobustMessage)

	sessionId = strconv.FormatInt(session.Id, 10)
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
		api.output.InterruptGetNext()

		if !wasSuperseded {
			api.deleteGetMessagesRequests(sessionId)
		}
	}

	api.setGetMessagesRequests(sessionId, GetMessagesStats{
		RemoteAddr:    r.RemoteAddr,
		Session:       session,
		Nick:          api.ircServer.GetNick(session),
		Started:       time.Now(),
		UserAgent:     r.Header.Get("User-Agent"),
		ForwardedFor:  r.Header.Get("X-Forwarded-For"),
		TrustedBridge: api.ircServer.TrustedBridge(r.Header.Get("X-Bridge-Auth")),
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
				if msg.Type != types.RobustPing && !msg.InterestingFor[session.Id] {
					continue
				}

				if err := enc.Encode(msg); err != nil {
					log.Printf("Error encoding JSON: %v\n", err)
					return
				}
			}

			if _, err := api.ircServer.GetSession(session); err != nil {
				// Session was deleted in the meanwhile, abort this request.
				return
			}

			api.updateLastContact()

			// The 10 seconds threshold is arbitrary. The only criterion is
			// that it must be higher than raft’s HeartbeatTimeout of 2s. The
			// higher it is chosen, the longer users have to wait until they
			// can connect to a different node. Note that in the worst case,
			// |pingInterval| = 20s needs to pass before this threshold is
			// evaluated.
			if api.raftNode.State() != raft.Leader && time.Since(lastContact) > 10*time.Second {
				// This node is neither the leader nor was it recently in
				// contact with the master, indicating that it is partitioned
				// from the rest of the network. We abort this GetMessages
				// request so that clients can connect to a different server
				// and receive new messages.
				log.Printf("Aborting GetMessages request due to LastContact (%v) too long ago\n", api.raftNode.LastContact())
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
