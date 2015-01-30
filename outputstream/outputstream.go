// Package outputstream represents the messages which the ircserver package
// generates in response to what is being sent to RobustIRC.
//
// The data structure and functions are carefully constructed so that messages
// can be read with high performance, yet old messages can be deleted and
// consumers can resume anywhere in the message stream.
package outputstream

import (
	"log"
	"math"
	"sort"
	"sync"

	"github.com/robustirc/robustirc/types"
)

// int64Slice is like sort.IntSlice, but for int64.
type int64Slice []int64

func (p int64Slice) Len() int           { return len(p) }
func (p int64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p int64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

type messageBatch struct {
	Messages []*types.RobustMessage

	// nextId is the id of the next message, or math.MaxInt64 if there is no
	// next message yet.
	nextId int64
}

var (
	// messagesMu guards messages and lastseen (pointer into messages).
	messagesMu sync.RWMutex
	newMessage = sync.NewCond(&messagesMu)

	// The uint64 represents the Id (not the Reply) of a types.RobustId. We use
	// an uint64 directly because then we can use the Go runtime’s
	// mapaccessN_fast64 code path.
	messages map[int64]*messageBatch
	lastseen *messageBatch
)

// Reset deletes all messages.
func Reset() {
	messagesMu.Lock()
	defer messagesMu.Unlock()

	messages = make(map[int64]*messageBatch)
	lastseen = &messageBatch{nextId: math.MaxInt64}
	messages[0] = lastseen
}

// Add adds messages to the output stream. The Id.Id field of all messages must
// be identical, i.e. they must all be replies to the same input IRC message.
func Add(msgs []*types.RobustMessage) {
	messagesMu.Lock()
	defer messagesMu.Unlock()
	lastseen.nextId = msgs[0].Id.Id
	lastseen = &messageBatch{
		Messages: msgs,
		nextId:   math.MaxInt64,
	}
	messages[msgs[0].Id.Id] = lastseen
	newMessage.Broadcast()
}

func LastSeen() types.RobustId {
	messagesMu.RLock()
	defer messagesMu.RUnlock()
	if len(lastseen.Messages) > 0 {
		return lastseen.Messages[0].Id
	} else {
		return types.RobustId{Id: 0}
	}
}

// Delete deletes all IRC output messages that were generated in reply to the
// input message with inputId.
func Delete(inputId types.RobustId) {
	messagesMu.Lock()
	defer messagesMu.Unlock()
	if messages[inputId.Id] == lastseen {
		if len(messages) == 1 {
			// We should always have the first message (RobustId{Id: 0}).
			log.Panicf("Delete() called on _all_ messages")
		}
		// When deleting the last message, lastseen needs to be set to the
		// previous message to avoid blocking in GetNext() forever.
		keys := make([]int64, len(messages))
		var i int
		for key, _ := range messages {
			keys[i] = key
			i++
		}
		sort.Sort(int64Slice(keys))
		lastseen = messages[keys[len(keys)-2]]
		lastseen.nextId = math.MaxInt64
	}
	delete(messages, inputId.Id)
}

// GetNext returns the next IRC output message after lastseen, even if lastseen
// was deleted in the meanwhile. In case there is no next message yet,
// GetNext blocks until it appears.
// GetNext(types.RobustId{Id: 0}) returns the first message.
func GetNext(lastseen types.RobustId) []*types.RobustMessage {
	// GetNext handles 4 different cases:
	//
	// ┌──────────────────┬───────────┬───────────────────────────────────────┐
	// │ lastseen message │  nextid   │ outcome                               │
	// ├──────────────────┼───────────┼───────────────────────────────────────┤
	// │    exists        │   valid   │ return                                │
	// │    exists        │ not found │ binary search for more recent message │
	// │    exists        │  MaxInt64 │ block until next message              │
	// │   not found      │     /     │ binary search for more recent message │
	// └──────────────────┴───────────┴───────────────────────────────────────┘
	//
	// Note that binary search may fall-through to blocking in case it cannot
	// find a more recent message.

	messagesMu.RLock()
	current, ok := messages[lastseen.Id]
	if ok && current.nextId < math.MaxInt64 {
		next, okNext := messages[current.nextId]
		if okNext {
			messagesMu.RUnlock()
			return next.Messages
		}
		// nextId points to a deleted message, fall back to binary search.
		ok = false
	}

	if !ok {
		// We don’t keep a sorted version of the keys around because this code
		// path is rarely taken.
		// TODO: add a counter
		keys := make([]int64, len(messages))
		var i int
		for key, _ := range messages {
			keys[i] = key
			i++
		}
		sort.Sort(int64Slice(keys))
		nextIdx := sort.Search(len(keys), func(i int) bool {
			return keys[i] > lastseen.Id
		})
		// There is an output message which is more recent than lastseen.
		if nextIdx < len(keys) {
			msg := messages[keys[nextIdx]]
			messagesMu.RUnlock()
			return msg.Messages
		}
		// There is no message which is more recent than lastseen, so just take
		// the last message and fallthrough into the code path that waits for
		// newer messages.
		current = messages[keys[len(keys)-1]]
	}
	messagesMu.RUnlock()

	// Wait until a new message appears.
	messagesMu.Lock()
	for {
		next, ok := messages[current.nextId]
		if ok {
			messagesMu.Unlock()
			return next.Messages
		}
		newMessage.Wait()
	}
}
