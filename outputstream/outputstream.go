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

	// nextID is the id of the next message, or math.MaxInt64 if there is no
	// next message yet.
	nextID int64
}

type OutputStream struct {
	// messagesMu guards messages and lastseen (pointer into messages).
	messagesMu sync.RWMutex
	newMessage *sync.Cond

	// The uint64 represents the Id (not the Reply) of a types.RobustId. We use
	// an uint64 directly because then we can use the Go runtime’s
	// mapaccessN_fast64 code path.
	messages map[int64]*messageBatch
	lastseen *messageBatch
}

func NewOutputStream() *OutputStream {
	os := &OutputStream{}
	os.newMessage = sync.NewCond(&os.messagesMu)
	os.Reset()
	return os
}

// Reset deletes all messages.
func (os *OutputStream) Reset() {
	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()

	os.messages = make(map[int64]*messageBatch)
	os.lastseen = &messageBatch{nextID: math.MaxInt64}
	os.messages[0] = os.lastseen
}

// Add adds messages to the output stream. The Id.Id field of all messages must
// be identical, i.e. they must all be replies to the same input IRC message.
func (os *OutputStream) Add(msgs []*types.RobustMessage) {
	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()
	os.lastseen.nextID = msgs[0].Id.Id
	os.lastseen = &messageBatch{
		Messages: msgs,
		nextID:   math.MaxInt64,
	}
	os.messages[msgs[0].Id.Id] = os.lastseen
	os.newMessage.Broadcast()
}

func (os *OutputStream) LastSeen() types.RobustId {
	os.messagesMu.RLock()
	defer os.messagesMu.RUnlock()
	if len(os.lastseen.Messages) > 0 {
		return os.lastseen.Messages[0].Id
	}
	return types.RobustId{Id: 0}
}

// Delete deletes all IRC output messages that were generated in reply to the
// input message with inputID.
func (os *OutputStream) Delete(inputID types.RobustId) {
	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()
	if os.messages[inputID.Id] == os.lastseen {
		if len(os.messages) == 1 {
			// We should always have the first message (RobustId{Id: 0}).
			log.Panicf("Delete() called on _all_ messages")
		}
		// When deleting the last message, lastseen needs to be set to the
		// previous message to avoid blocking in GetNext() forever.
		keys := make([]int64, len(os.messages))
		var i int
		for key := range os.messages {
			keys[i] = key
			i++
		}
		sort.Sort(int64Slice(keys))
		os.lastseen = os.messages[keys[len(keys)-2]]
		os.lastseen.nextID = math.MaxInt64
	}
	delete(os.messages, inputID.Id)
}

// GetNext returns the next IRC output message after lastseen, even if lastseen
// was deleted in the meanwhile. In case there is no next message yet,
// GetNext blocks until it appears.
// GetNext(types.RobustId{Id: 0}) returns the first message.
func (os *OutputStream) GetNext(lastseen types.RobustId) []*types.RobustMessage {
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

	os.messagesMu.RLock()
	current, ok := os.messages[lastseen.Id]
	if ok && current.nextID < math.MaxInt64 {
		next, okNext := os.messages[current.nextID]
		if okNext {
			os.messagesMu.RUnlock()
			return next.Messages
		}
		// nextID points to a deleted message, fall back to binary search.
		ok = false
	}

	if !ok {
		// We don’t keep a sorted version of the keys around because this code
		// path is rarely taken.
		// TODO: add a counter
		keys := make([]int64, len(os.messages))
		var i int
		for key := range os.messages {
			keys[i] = key
			i++
		}
		sort.Sort(int64Slice(keys))
		nextIDx := sort.Search(len(keys), func(i int) bool {
			return keys[i] > lastseen.Id
		})
		// There is an output message which is more recent than lastseen.
		if nextIDx < len(keys) {
			msg := os.messages[keys[nextIDx]]
			os.messagesMu.RUnlock()
			return msg.Messages
		}
		// There is no message which is more recent than lastseen, so just take
		// the last message and fallthrough into the code path that waits for
		// newer messages.
		current = os.messages[keys[len(keys)-1]]
	}
	os.messagesMu.RUnlock()

	// Wait until a new message appears.
	os.messagesMu.Lock()
	for {
		next, ok := os.messages[current.nextID]
		if ok {
			os.messagesMu.Unlock()
			return next.Messages
		}
		os.newMessage.Wait()
	}
}

// Get returns the next IRC output message for 'input', if present.
func (os *OutputStream) Get(input types.RobustId) ([]*types.RobustMessage, bool) {
	os.messagesMu.RLock()
	defer os.messagesMu.RUnlock()
	output, ok := os.messages[input.Id]
	if !ok {
		return nil, ok
	}
	return output.Messages, ok
}
