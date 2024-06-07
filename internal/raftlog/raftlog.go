// Package raftlog provides helper functions for (un)marshaling raft.Log.
package raftlog

import (
	"encoding/json"
	"fmt"

	"github.com/golang/protobuf/proto"
	"github.com/hashicorp/raft"

	pb "github.com/robustirc/robustirc/internal/proto"
)

// FromBytes returns a raft.Log unmarshaled from the provided bytes.
func FromBytes(b []byte) (*raft.Log, error) {
	var l raft.Log
	if len(b) == 0 {
		return nil, fmt.Errorf("invalid raft.Log message: 0 bytes")
	}
	if b[0] == 'p' {
		var p pb.RaftLog
		if err := proto.Unmarshal(b[1:], &p); err != nil {
			return nil, err
		}
		l.Index = p.Index
		l.Term = p.Term
		l.Type = raft.LogType(p.Type)
		l.Data = p.Data
		l.Extensions = p.Extensions
		l.AppendedAt = p.AppendedAt.AsTime()
	} else {
		// XXX(1.0): delete this branch, proto is used everywhere
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, err
		}
	}
	return &l, nil
}
