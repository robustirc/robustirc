// Package outputstream represents the messages which the ircserver package
// generates in response to what is being sent to RobustIRC.
//
// Data is stored in a temporary LevelDB database so that not all data is kept
// in main memory at all times. The working set we are talking about is ≈100M,
// but using LevelDB (with its default Snappy compression), that gets
// compressed down to ≈35M.
package outputstream

import (
	"encoding/binary"
	"io/ioutil"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/robustirc/robustirc/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Message is similar to types.RobustMessage, but more compact. This speeds up
// (de)serialization, which is useful for storing messages outside of main
// memory.
type Message struct {
	Id             types.RobustId
	Data           string
	InterestingFor map[int64]bool
}

type messageBatch struct {
	Messages []Message

	// NextID is the id of the next message, or math.MaxUint64 if there is no
	// next message yet.
	NextID uint64
}

type OutputStream struct {
	tmpdir string

	// messagesMu guards |db|, |batch| and |lastseen|.
	messagesMu sync.RWMutex
	newMessage *sync.Cond

	db       *leveldb.DB
	batch    leveldb.Batch
	lastseen messageBatch

	cacheMu       sync.RWMutex
	messagesCache map[uint64]*messageBatch
}

func DeleteOldDatabases(tmpdir string) error {
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
		if strings.HasPrefix(name, "tmp-outputstream-") {
			if err := os.RemoveAll(filepath.Join(tmpdir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func NewOutputStream(tmpdir string) (*OutputStream, error) {
	os := &OutputStream{
		tmpdir:        tmpdir,
		messagesCache: make(map[uint64]*messageBatch),
	}
	os.newMessage = sync.NewCond(&os.messagesMu)
	return os, os.Reset()
}

// Reset deletes all messages.
func (os *OutputStream) Reset() error {
	var key [8]byte

	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()

	if os.db != nil {
		if err := os.db.Close(); err != nil {
			return err
		}
	}

	dirname, err := ioutil.TempDir(os.tmpdir, "tmp-outputstream-")
	if err != nil {
		return err
	}

	// Open a temporary database, i.e. one whose values we only use as long as
	// this RobustIRC process is running, and which will be deleted at the next
	// startup. This implies we don’t need to fsync(), and leveldb should error
	// out when there already is a database in our newly created tempdir.
	db, err := leveldb.OpenFile(dirname, &opt.Options{
		ErrorIfExist:       true,
		NoSync:             true,
		BlockCacheCapacity: 2 * 1024 * 1024,
	})
	if err != nil {
		return err
	}
	os.db = db

	os.lastseen = messageBatch{
		Messages: []Message{
			{
				Id:             types.RobustId{Id: 0},
				InterestingFor: make(map[int64]bool),
			},
		},
		NextID: math.MaxUint64,
	}
	binary.BigEndian.PutUint64(key[:], uint64(0))
	return os.db.Put(key[:], os.lastseen.marshal(), nil)
}

// Add adds messages to the output stream. The Id.Id field of all messages must
// be identical, i.e. they must all be replies to the same input IRC message.
func (os *OutputStream) Add(msgs []Message) error {
	var key [8]byte

	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()

	os.batch.Reset()

	os.lastseen.NextID = uint64(msgs[0].Id.Id)
	binary.BigEndian.PutUint64(key[:], uint64(os.lastseen.Messages[0].Id.Id))
	os.batch.Put(key[:], os.lastseen.marshal())
	os.cacheMu.Lock()
	delete(os.messagesCache, uint64(os.lastseen.Messages[0].Id.Id))
	os.cacheMu.Unlock()

	os.lastseen = messageBatch{
		Messages: msgs,
		NextID:   math.MaxUint64,
	}
	binary.BigEndian.PutUint64(key[:], uint64(msgs[0].Id.Id))
	os.batch.Put(key[:], os.lastseen.marshal())
	if err := os.db.Write(&os.batch, nil); err != nil {
		return err
	}

	os.newMessage.Broadcast()
	return nil
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
func (os *OutputStream) Delete(inputID types.RobustId) error {
	var key [8]byte

	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()
	if inputID.Id == os.lastseen.Messages[0].Id.Id {
		// When deleting the last message, lastseen needs to be set to the
		// previous message to avoid blocking in GetNext() forever.
		i := os.db.NewIterator(nil, nil)
		defer i.Release()
		if !i.Last() {
			log.Panicf("outputstream LevelDB is empty, which is a BUG\n")
		}
		if !i.Prev() {
			// We should always keep the first message (RobustId{Id: 0}).
			log.Panicf("Delete() called on _all_ messages\n")
		}

		mb := unmarshalMessageBatch(i.Value())
		os.lastseen = messageBatch{
			Messages: mb.Messages,
			NextID:   math.MaxUint64,
		}

		binary.BigEndian.PutUint64(key[:], uint64(os.lastseen.Messages[0].Id.Id))
		if err := os.db.Put(key[:], os.lastseen.marshal(), nil); err != nil {
			return err
		}
	}
	os.cacheMu.Lock()
	delete(os.messagesCache, uint64(inputID.Id))
	os.cacheMu.Unlock()
	binary.BigEndian.PutUint64(key[:], uint64(inputID.Id))
	return os.db.Delete(key[:], nil)
}

// GetNext returns the next IRC output message after lastseen, even if lastseen
// was deleted in the meanwhile. In case there is no next message yet,
// GetNext blocks until it appears.
// GetNext(types.RobustId{Id: 0}) returns the first message.
func (os *OutputStream) GetNext(lastseen types.RobustId, cancelled *bool) []Message {
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
	current, ok := os.getUnlocked(uint64(lastseen.Id))
	if ok && current.NextID < math.MaxUint64 {
		next, okNext := os.getUnlocked(current.NextID)
		if okNext {
			os.messagesMu.RUnlock()
			return next.Messages
		}
		// NextID points to a deleted message, fall back to binary search.
		ok = false
	}

	if !ok {
		// Anything _newer_ than lastseen, i.e. the interval [lastseen.Id+1, ∞)
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], uint64(lastseen.Id)+1)
		i := os.db.NewIterator(&util.Range{
			Start: key[:],
			Limit: nil,
		}, nil)
		defer i.Release()
		if i.First() {
			mb := unmarshalMessageBatch(i.Value())
			os.messagesMu.RUnlock()
			return mb.Messages
		}

		// There is no message which is more recent than lastseen, so just take
		// the last message and fallthrough into the code path that waits for
		// newer messages.
		i = os.db.NewIterator(nil, nil)
		defer i.Release()
		if !i.Last() {
			log.Panicf("outputstream LevelDB is empty, which is a BUG\n")
		}

		current = unmarshalMessageBatch(i.Value())
	}
	os.messagesMu.RUnlock()

	// Wait until a new message appears.
	os.messagesMu.Lock()
	for {
		current, _ = os.getUnlocked(uint64(current.Messages[0].Id.Id))
		next, ok := os.getUnlocked(current.NextID)
		if ok {
			os.messagesMu.Unlock()
			return next.Messages
		}
		if cancelled != nil && *cancelled {
			os.messagesMu.Unlock()
			return []Message{}
		}
		os.newMessage.Wait()
	}
}

// InterruptGetNext interrupts any running GetNext() calls so that they return
// if |cancelled| is specified and true in the GetNext() call.
func (os *OutputStream) InterruptGetNext() {
	os.messagesMu.Lock()
	defer os.messagesMu.Unlock()
	os.newMessage.Broadcast()
}

func (os *OutputStream) getUnlocked(id uint64) (*messageBatch, bool) {
	var key [8]byte
	os.cacheMu.RLock()
	mb, ok := os.messagesCache[id]
	os.cacheMu.RUnlock()
	if ok {
		return mb, ok
	}
	binary.BigEndian.PutUint64(key[:], id)
	value, err := os.db.Get(key[:], nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, false
		}
		log.Panicf("Unexpected outputstream LevelDB error: %v\n", err)
	}
	mb = unmarshalMessageBatch(value)
	os.cacheMu.Lock()
	// A cache size of 1000 has empirically worked best so far.
	if len(os.messagesCache) > 1000 {
		// Just randomly delete entries to free up memory.
		for id := range os.messagesCache {
			delete(os.messagesCache, id)
			if len(os.messagesCache) < 500 {
				break
			}
		}
	}
	os.messagesCache[id] = mb
	os.cacheMu.Unlock()
	return mb, true
}

// Get returns the next IRC output message for 'input', if present.
func (os *OutputStream) Get(input types.RobustId) ([]Message, bool) {
	os.messagesMu.RLock()
	defer os.messagesMu.RUnlock()

	mb, ok := os.getUnlocked(uint64(input.Id))
	if !ok {
		return nil, ok
	}
	return mb.Messages, true
}
