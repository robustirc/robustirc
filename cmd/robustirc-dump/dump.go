// dump dumps all of the data that RobustIRC persists on disk,
// i.e. raftlog/, irclog/ and subdirectories of snapshots/.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/types"
	"github.com/robustirc/robustirc/util"
	"github.com/syndtr/goleveldb/leveldb"
	leveldb_errors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

var (
	path = flag.String("path",
		"",
		"Path to the database directory to dump.")

	onlyCompacted = flag.Bool("only_compacted",
		false,
		"Display only messages which (should have) undergone at least one compaction cycle because of their age.")

	padding = len(fmt.Sprintf("%d", uint64(math.MaxUint64)))
	format  = fmt.Sprintf("%%%dd\t%%s\t%%s\t%%v (%%v)\t%%s\t%%s", padding) + "\n"

	lastId       int64
	lastModified = time.Now()
)

func dumpLog(key uint64, rlog *raft.Log) {
	if rlog.Type != raft.LogCommand {
		// TODO: hexdump
		log.Printf("type == %d, data = %s\n", rlog.Type, string(rlog.Data))
		return
	}

	unfilteredMsg := types.NewRobustMessageFromBytes(rlog.Data)
	rmsg := &unfilteredMsg
	if rmsg.Type == types.RobustIRCFromClient {
		rmsg = util.PrivacyFilterMsg(rmsg)
	}
	msgtime := time.Unix(0, rmsg.Id.Id)
	timepassed := lastModified.Sub(msgtime)
	if !*onlyCompacted || timepassed > 7*24*time.Hour {
		fmt.Printf(format, key, rmsg.Type, rmsg.Id.String(), msgtime, timepassed, rmsg.Session.String(), rmsg.Data)
	}

	if rmsg.Id.Id < lastId {
		log.Printf("WARNING: message IDs not strictly monotonically increasing at %v\n", time.Unix(0, rmsg.Id.Id))
	}
	lastId = rmsg.Id.Id
}

func dumpLeveldb(path string) error {
	db, err := leveldb.OpenFile(path, &opt.Options{
		ErrorIfMissing: true,
	})
	if err != nil {
		if _, ok := err.(*leveldb_errors.ErrCorrupted); !ok {
			return err
		}
		log.Printf("Database is corrupted, trying to recover\n")
		db, err = leveldb.RecoverFile(path, nil)
		if err != nil {
			return fmt.Errorf("Could not recover database: %v\n", err)
		}
	}
	defer db.Close()

	i := db.NewIterator(nil, nil)
	defer i.Release()

	var rlog raft.Log

	if i.Last() {
		for bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
			i.Prev()
		}
		for {
			if err := json.Unmarshal(i.Value(), &rlog); err != nil {
				log.Fatalf("Corrupted database: %v\n", err)
			}
			if rlog.Type == raft.LogCommand {
				rmsg := types.NewRobustMessageFromBytes(rlog.Data)
				lastModified = time.Unix(0, rmsg.Id.Id)
				break
			}
			i.Prev()
		}
	}
	i.First()

	fmt.Printf(fmt.Sprintf("%%%ds", padding)+"\tValue\n", "Key")

	for i.Next() {
		if bytes.HasPrefix(i.Key(), []byte("stablestore-")) {
			// TODO: also dump the stablestore values
		} else {
			if err := json.Unmarshal(i.Value(), &rlog); err != nil {
				log.Fatalf("Corrupted database: %v\n", err)
			}
			dumpLog(binary.BigEndian.Uint64(i.Key()), &rlog)
		}
	}

	return nil
}

func dumpSnapshot(path string) error {
	metaPath := filepath.Join(path, "meta.json")
	fi, err := os.Stat(metaPath)
	if err != nil {
		return fmt.Errorf("%q missing: %v", metaPath, err)
	}
	lastModified = fi.ModTime()
	log.Printf("trying to dump %q as a snapshot\n", path)
	f, err := os.Open(filepath.Join(path, "state.bin"))
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	var rlog raft.Log
	for {
		if err := decoder.Decode(&rlog); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		dumpLog(0, &rlog)
	}
}

func main() {
	flag.Parse()

	if strings.TrimSpace(*path) == "" {
		log.Fatalf("specifying -path is required\n")
	}

	leveldbErr := dumpLeveldb(*path)
	if leveldbErr == nil {
		return
	}
	snapshotErr := dumpSnapshot(*path)
	if snapshotErr != nil {
		log.Fatalf("Path %q contains neither a LevelDB database (OpenFile: %v) nor a raft snapshot (%v)\n", *path, leveldbErr, snapshotErr)
	}
}
