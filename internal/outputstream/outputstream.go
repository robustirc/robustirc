// Package outputstream represents the messages which the ircserver package
// generates in response to what is being sent to RobustIRC.
//
// Data is cached in main memory, then spilled to disk. The working set is
// usually about 100MB, and could be compressed down to 35MB.
package outputstream

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/net/context"

	"github.com/robustirc/robustirc/internal/robust"
)

// Message is similar to robust.Message, but more compact. This speeds up
// (de)serialization, which is useful for storing messages outside of main
// memory.
type Message struct {
	Id             robust.Id
	Data           string
	InterestingFor map[uint64]bool
}

type messageBatch struct {
	Messages []Message

	// NextID is the id of the next message, or math.MaxUint64 if there is no
	// next message yet.
	NextID uint64
}

type fileHandle struct {
	firstID uint64
	lastID  uint64
	file    *os.File
}

type OutputStream struct {
	// tmpdir is the directory which we pass to ioutil.TempDir.
	tmpdir string

	// dirname is the directory returned by ioutil.TempDir which
	// contains our database.
	dirname string

	newMessage *sync.Cond

	cacheCapacity int64

	cacheMu       sync.RWMutex
	messagesCache []*messageBatch

	filesMu sync.RWMutex
	files   []fileHandle
}

func DeleteOldDatabases(tmpdir string) error {
	matches, err := filepath.Glob(filepath.Join(tmpdir, "tmp-outputstream-*"))
	if err != nil {
		return err
	}
	for _, name := range matches {
		if err := os.RemoveAll(filepath.Join(tmpdir, name)); err != nil {
			return err
		}
	}
	return nil
}

// assumptions:
// • message IDs are strictly monotonically increasing, but not consecutive
//   (not all raft messages are irc messages, and hence not all raft messages
//    have an output).
// • messages are appended only
// • messages are deleted in batches

func NewOutputStream(tmpdir string, cacheCapacity int64) (*OutputStream, error) {
	os := &OutputStream{
		tmpdir:        tmpdir,
		cacheCapacity: cacheCapacity,
	}
	var err error
	os.dirname, err = ioutil.TempDir(tmpdir, "tmp-outputstream-")
	if err != nil {
		return nil, err
	}
	os.newMessage = sync.NewCond(&os.cacheMu)
	return os, os.reset()
}

func (o *OutputStream) Close() error {
	return os.RemoveAll(o.dirname)
}

// Reset deletes all messages.
func (o *OutputStream) reset() error {
	o.messagesCache = make([]*messageBatch, 0, o.cacheCapacity)
	return nil
}

func (o *OutputStream) indexBytes() int64 { return (o.cacheCapacity + 1) * 8 }

// Add adds messages to the output stream. The Id.Id field of all messages must
// be identical, i.e. they must all be replies to the same input IRC message.
func (o *OutputStream) Add(msgs []Message) error {
	o.cacheMu.Lock()
	defer o.cacheMu.Unlock()
	batch := messageBatch{
		Messages: msgs,
	}

	if len(o.messagesCache) == cap(o.messagesCache) {
		// Cache full, flush to disk.
		fh := fileHandle{
			firstID: o.messagesCache[0].Messages[0].Id.Id,
			lastID:  o.messagesCache[len(o.messagesCache)-1].Messages[0].Id.Id,
		}
		f, err := os.Create(filepath.Join(o.dirname, fmt.Sprintf("%d_to_%d.bin", fh.firstID, fh.lastID)))
		if err != nil {
			return err
		}
		fh.file = f

		// Reserve space for the index. We store one extra entry so that we can
		// unconditionally read offset and nextOffset below (to get the length).
		offsets := make([]uint64, len(o.messagesCache)+1)
		if _, err := f.Seek(o.indexBytes(), io.SeekStart); err != nil {
			return err
		}

		// Write the messages, updating the index as we go.
		var offset uint64
		for idx, mb := range o.messagesCache {
			b := mb.marshal()
			if _, err := f.Write(b); err != nil {
				return err
			}
			offsets[idx] = offset
			offset += uint64(len(b))
		}
		offsets[len(o.messagesCache)] = offset

		// Write the index.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}

		for _, offset := range offsets {
			if err := binary.Write(f, binary.BigEndian, offset); err != nil {
				return err
			}
		}

		o.filesMu.Lock()
		defer o.filesMu.Unlock()
		o.files = append(o.files, fh)
		o.messagesCache = make([]*messageBatch, 0, o.cacheCapacity)
	}
	o.messagesCache = append(o.messagesCache, &batch)

	o.newMessage.Broadcast()
	return nil
}

// Delete deletes all IRC output messages that were generated up to (and including) last.
func (o *OutputStream) Delete(last robust.Id) error {
	o.filesMu.Lock()
	defer o.filesMu.Unlock()
	newFiles := make([]fileHandle, 0, len(o.files))
	for idx, fh := range o.files {
		if last.Id >= fh.lastID {
			fh.file.Close()
			if err := os.Remove(fh.file.Name()); err != nil {
				return err
			}
			continue
		}
		newFiles = append(newFiles, o.files[idx:]...)
		break
	}
	o.files = newFiles
	return nil
}

// GetNext returns the next IRC output message after lastseen, even if lastseen
// was deleted in the meanwhile. In case there is no next message yet,
// GetNext blocks until it appears.
// GetNext(types.RobustId{Id: 0}) returns the first message.
func (o *OutputStream) GetNext(ctx context.Context, lastseen robust.Id) []Message {
	nextId := robust.Id{Id: lastseen.Id + 1}
	mb, ok := o.Get(nextId)
	if ok {
		return mb
	}

	// Wait until a new message appears.
	o.cacheMu.Lock()
	for {
		next, ok := o.getUnlocked(nextId.Id)
		if ok {
			o.cacheMu.Unlock()
			return next.Messages
		}
		select {
		case <-ctx.Done():
			o.cacheMu.Unlock()
			return []Message{}
		default:
		}
		o.newMessage.Wait()
	}
}

// InterruptGetNext interrupts any running GetNext() calls so that they return
// if |cancelled| is specified and true in the GetNext() call.
func (o *OutputStream) InterruptGetNext() {
	o.cacheMu.Lock()
	defer o.cacheMu.Unlock()
	o.newMessage.Broadcast()
}

func (o *OutputStream) getCachedUnlocked(id uint64) (*messageBatch, bool) {
	n := len(o.messagesCache)
	if n == 0 {
		return nil, false
	}

	if id < o.messagesCache[0].Messages[0].Id.Id {
		// not cached anymore
		return nil, false
	}

	i := sort.Search(n, func(i int) bool {
		return o.messagesCache[i].Messages[0].Id.Id >= id
	})
	if i == n {
		// not found
		return nil, false
	}
	return o.messagesCache[i], true
}

func (o *OutputStream) getUnlocked(id uint64) (*messageBatch, bool) {
	if mb, ok := o.getCachedUnlocked(id); ok {
		return mb, ok
	}

	o.filesMu.RLock()
	defer o.filesMu.RUnlock()
	// walk files in reverse, because newer messages are more likely to be requested
	for idx := len(o.files) - 1; idx >= 0; idx-- {
		fh := o.files[idx]
		if id < fh.firstID || id > fh.lastID {
			continue
		}

		if _, err := fh.file.Seek(int64((id-fh.firstID)*8), io.SeekStart); err != nil {
			return nil, false
		}
		var offset, nextOffset uint64
		if err := binary.Read(fh.file, binary.BigEndian, &offset); err != nil {
			return nil, false
		}
		if err := binary.Read(fh.file, binary.BigEndian, &nextOffset); err != nil {
			return nil, false
		}

		log.Printf("id = %d, firstID = %d, lastID = %d, offset = %d, nextOffset = %d", id, fh.firstID, fh.lastID, offset, nextOffset)
		if _, err := fh.file.Seek(o.indexBytes()+int64(offset), io.SeekStart); err != nil {
			return nil, false
		}
		b := make([]byte, nextOffset-offset)
		if _, err := fh.file.Read(b); err != nil {
			return nil, false
		}
		mb := unmarshalMessageBatch(b)
		return mb, true
	}
	return nil, false
}

// Get returns the IRC output message for 'input', if present.
func (o *OutputStream) Get(input robust.Id) ([]Message, bool) {
	o.cacheMu.RLock()
	defer o.cacheMu.RUnlock()

	mb, ok := o.getUnlocked(input.Id)
	if !ok {
		return nil, false
	}
	return mb.Messages, true
}
