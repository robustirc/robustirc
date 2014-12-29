package main

import (
	"encoding/binary"
	"io/ioutil"
	"log"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/armon/gomdb"
)

const (
	timeDB  = "time"    // map of emit time to entry uuid
	keyDB   = "keys"    // map of user provided key to entry uuid. optional
	entryDB = "entries" // map of uuid to entry

	dbMaxMapSize32bit uint64 = 512 * 1024 * 1024       // 512MB maximum size
	dbMaxMapSize64bit uint64 = 32 * 1024 * 1024 * 1024 // 32GB maximum size
)

var allDBs = []string{timeDB, keyDB, entryDB}

func bytesToUint64(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}

func uint64ToBytes(u uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, u)
	return buf
}

// Storage is the database backend for persisting Entries via LMDB, and triggering
// entry emission.
type Storage struct {
	env *mdb.Env
	dbi *mdb.DBI
	dir string

	C <-chan time.Time
	c chan<- time.Time
}

// NewStorage creates a new Storage instance. prefix is the base directory where
// data is written on-disk.
func NewStorage() (s *Storage, err error) {
	s = new(Storage)

	err = s.initDB()
	if err != nil {
		return
	}

	c := make(chan time.Time, 10)
	s.C = c
	s.c = c

	return
}

func (s *Storage) initDB() (err error) {
	s.env, err = mdb.NewEnv()
	if err != nil {
		return
	}

	// Set the maximum db size based on 32/64bit
	dbSize := dbMaxMapSize32bit
	if runtime.GOARCH == "amd64" {
		dbSize = dbMaxMapSize64bit
	}

	// Increase the maximum map size
	err = s.env.SetMapSize(dbSize)
	if err != nil {
		return
	}

	s.dir, err = ioutil.TempDir("", "delayd")
	if err != nil {
		return
	}

	storageDir := path.Join(s.dir, "db")
	err = os.MkdirAll(storageDir, 0755)
	if err != nil {
		return
	}
	log.Printf("Created temporary storage directory:", storageDir)

	// 3 sub dbs: Entries, time index, and key index
	err = s.env.SetMaxDBs(mdb.DBI(3))
	if err != nil {
		return
	}

	// Optimize our flags for speed over safety. Raft handles durable storage.
	var flags uint
	flags = mdb.NOMETASYNC | mdb.NOSYNC | mdb.NOTLS

	err = s.env.Open(storageDir, flags, 0755)
	if err != nil {
		return
	}

	// Initialize sub dbs
	txn, _, err := s.startTxn(false, allDBs...)
	if err != nil {
		txn.Abort()
		return
	}

	err = txn.Commit()
	if err != nil {
		return
	}

	return
}

// Close gracefully shuts down a storage instance. Before calling it, ensure
// that all in-flight requests have been processed.
func (s *Storage) Close() {
	// mdb will segfault if any transactions are in process when this is called,
	// so ensure users of storage are stopped first.
	s.env.Close()
	os.RemoveAll(s.dir)
}

// startTxn is used to start a transaction and open all the associated sub-databases
func (s *Storage) startTxn(readonly bool, open ...string) (txn *mdb.Txn, dbis []mdb.DBI, err error) {
	var txnFlags uint
	var dbiFlags uint
	if readonly {
		txnFlags |= mdb.RDONLY
	} else {
		dbiFlags |= mdb.CREATE
	}

	txn, err = s.env.BeginTxn(nil, txnFlags)
	if err != nil {
		return
	}

	for _, name := range open {
		// Allow duplicate entries for the same time
		realFlags := dbiFlags
		if name == timeDB {
			realFlags |= mdb.DUPSORT
		}

		var dbi mdb.DBI
		dbi, err = txn.DBIOpen(name, realFlags)
		if err != nil {
			txn.Abort()
			return
		}
		dbis = append(dbis, dbi)
	}

	return
}

// Add an Entry to the database.
func (s *Storage) Add(uuid []byte, e string) (err error) {
	txn, dbis, err := s.startTxn(false, timeDB, entryDB, keyDB)
	if err != nil {
		return
	}

	k := uint64ToBytes(uint64(time.Now().UnixNano()))
	err = txn.Put(dbis[0], k, uuid, 0)
	if err != nil {
		txn.Abort()
		return
	}

	b := []byte(e)
	err = txn.Put(dbis[1], uuid, b, 0)
	if err != nil {
		txn.Abort()
		return
	}

	ok, t, err := s.innerNextTime(txn, dbis[0])
	if err != nil {
		txn.Abort()
		return
	}

	err = txn.Commit()

	if err == nil && ok {
		s.c <- t
	}

	return
}

func (s *Storage) innerGet(t time.Time, all bool) (uuids [][]byte, entries []string, err error) {
	txn, dbis, err := s.startTxn(true, timeDB, entryDB)
	if err != nil {
		log.Printf("Error creating transaction: ", err)
		return
	}
	defer txn.Abort()

	cursor, err := txn.CursorOpen(dbis[0])
	if err != nil {
		log.Printf("Error getting cursor for get entry: ", err)
		return
	}
	defer cursor.Close()

	sk := uint64(t.UnixNano())
	log.Printf("Looking for: ", t, t.UnixNano())

	for {
		var k, uuid, v []byte
		k, uuid, err = cursor.Get(nil, mdb.NEXT)
		if err == mdb.NotFound {
			err = nil
			break
		}
		if err != nil {
			return
		}

		if !all {
			kt := bytesToUint64(k)
			if kt > sk {
				err = nil
				break
			}
		}

		v, err = txn.Get(dbis[1], uuid)
		if err != nil {
			return
		}

		entry := string(v)
		entries = append(entries, entry)
		uuids = append(uuids, uuid)
	}

	return
}

// Get returns all entries that occur at or before the provided time
func (s *Storage) Get(t time.Time) (uuids [][]byte, entries []string, err error) {
	return s.innerGet(t, false)
}

// GetAll returns every entry in storage
func (s *Storage) GetAll() (uuids [][]byte, entries []string, err error) {
	return s.innerGet(time.Now(), true)
}

func (s *Storage) innerNextTime(txn *mdb.Txn, dbi mdb.DBI) (ok bool, t time.Time, err error) {
	ok = true
	cursor, err := txn.CursorOpen(dbi)
	if err != nil {
		log.Printf("Error getting cursor for next time: ", err)
		return
	}
	defer cursor.Close()

	k, _, err := cursor.Get(nil, mdb.FIRST)
	if err == mdb.NotFound {
		err = nil
		ok = false
		return
	}
	if err != nil {
		log.Printf("Error reading next time from db: ", err)
		return
	}

	t = time.Unix(0, int64(bytesToUint64(k)))
	return
}

// NextTime gets the next entry send time from the db
func (s *Storage) NextTime() (ok bool, t time.Time, err error) {
	txn, dbis, err := s.startTxn(true, timeDB)
	if err != nil {
		log.Printf("Error creating transaction: ", err)
		return
	}
	defer txn.Abort()

	ok, t, err = s.innerNextTime(txn, dbis[0])
	return
}

func (s *Storage) innerRemove(txn *mdb.Txn, dbis []mdb.DBI, uuid []byte) (err error) {
	be, err := txn.Get(dbis[1], uuid)
	if err != nil {
		log.Printf("Could not read entry: ", err)
		return
	}

	e := string(be)
	log.Printf("e = %v\n", e)
	k := uint64ToBytes(uint64(time.Now().UnixNano()))
	err = txn.Del(dbis[0], k, uuid)
	if err != nil {
		log.Printf("Could not delete from time series: ", err)
		return
	}

	err = txn.Del(dbis[1], uuid, nil)
	if err != nil {
		log.Printf("Could not delete entry: ", err)
		return
	}

	// check if the key exists before deleting.
	cursor, err := txn.CursorOpen(dbis[2])
	if err != nil {
		log.Printf("Error getting cursor for keys: ", err)
		return
	}
	defer cursor.Close()

	return
}

// Remove an emitted entry from the db. uuid is the Entry's UUID.
func (s *Storage) Remove(uuid []byte) (err error) {
	txn, dbis, err := s.startTxn(false, timeDB, entryDB, keyDB)
	if err != nil {
		return
	}

	err = s.innerRemove(txn, dbis, uuid)
	if err != nil {
		txn.Abort()
		return
	}

	ok, t, err := s.innerNextTime(txn, dbis[0])
	if err != nil {
		txn.Abort()
		return
	}

	err = txn.Commit()

	if err == nil && ok {
		s.c <- t
	}

	return
}
