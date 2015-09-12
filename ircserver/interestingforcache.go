package ircserver

import (
	"encoding/binary"
	"hash/fnv"
	"log"
	"sort"
	"unsafe"

	"github.com/robustirc/robustirc/types"
)

type uint64Slice []uint64

func (p uint64Slice) Len() int           { return len(p) }
func (p uint64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p uint64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// reuseInterestingFor replaces the InterestingFor fields of |reply.Messages|
// with pointers to the same InterestingFor fields of previously used messages,
// if they are the same.
func (i *IRCServer) reuseInterestingFor(reply *Replyctx) {
	encoded := make([]byte, unsafe.Sizeof(uint64(0)))
	h := fnv.New64a()
	for idx, msg := range reply.Messages {
		h.Reset()
		sessions := make([]uint64, 0, len(*msg.InterestingFor))
		for session, _ := range *msg.InterestingFor {
			sessions = append(sessions, uint64(session))
		}
		sort.Sort(uint64Slice(sessions))
		for _, session := range sessions {
			binary.LittleEndian.PutUint64(encoded, session)
			// (hash/fnv).sum64a.Write() always returns err == nil
			h.Write(encoded)
		}
		sum := h.Sum64()
		if m, ok := i.ifcache[sum]; ok {
			reply.Messages[idx].InterestingFor = m
		} else {
			i.ifcache[sum] = msg.InterestingFor
		}
	}
}

func (i *IRCServer) CleanupInterestingForCache() {
	cancelled := true
	// We invert |i.ifcache| so that we can avoid sorting and hashing the
	// message’s InterestingFor and rather can just do a map lookup to see if
	// the entry is in use.
	inverted := make(map[*map[int64]bool]uint64)
	for sum, m := range i.ifcache {
		inverted[m] = sum
	}

	ifused := make(map[uint64]*map[int64]bool)
	next := types.RobustId{Id: 0}
	for {
		out := i.GetNext(next, &cancelled)
		if len(out) == 0 {
			break
		}
		next = out[0].Id
		for _, msg := range out {
			if sum, ok := inverted[msg.InterestingFor]; ok {
				ifused[sum] = msg.InterestingFor
			}
		}
	}
	log.Printf("cleanup: len(ifcache) = %d, len(ifused) = %d\n", len(i.ifcache), len(ifused))
	i.ifcache = ifused
}
