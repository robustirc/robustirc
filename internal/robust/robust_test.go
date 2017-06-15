package robust_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/golang/protobuf/proto"
	"github.com/robustirc/robustirc/internal/robust"

	pb "github.com/robustirc/robustirc/internal/proto"
)

var msgs = []robust.Message{
	{
		Id:      robust.Id{Id: 0x40826d776b433c17, Reply: 3},
		Session: robust.Id{Id: 0x40826d776b433c17, Reply: 0},
		Type:    robust.IRCFromClient,
		Data:    "JOIN #robustirc",
	},

	{
		Id:      robust.Id{Id: 0x40826d776b433c17, Reply: 3},
		Session: robust.Id{Id: 0x40826d776b433c17, Reply: 0},
		InterestingFor: map[uint64]bool{
			0x40826d776b433c17: true,
		},
		Data: "JOIN #robustirc",
	},

	{
		Id:      robust.Id{Id: 0x40826d776b433c17, Reply: 3},
		Session: robust.Id{Id: 0x40826d776b433c17, Reply: 0},
		Servers: []string{
			"localhost:13001",
			"localhost:13002",
			"localhost:13003",
		},
		InterestingFor: map[uint64]bool{
			0x40826d776b433c17: true,
			0x40826d776b433c18: true,
			0x40826d776b433c19: true,
		},
		Currentmaster:   "localhost:13003",
		ClientMessageId: 0x234255,
		Revision:        5,
		Data:            "this is a longer message. this is a longer message. this is a longer message. this is a longer message. this is a longer message.",
	},
}

func BenchmarkEncodeJSON(b *testing.B) {
	buf := bytes.NewBuffer(make([]byte, 5*1024*1024))
	enc := json.NewEncoder(buf)
	l := len(msgs)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := enc.Encode(msgs[i%l]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeProto(b *testing.B) {
	buf := bytes.NewBuffer(make([]byte, 5*1024*1024))
	l := len(msgs)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m, err := proto.Marshal(msgs[i%l].ProtoMessage())
		if err != nil {
			b.Fatal(err)
		}
		if _, err := buf.Write(m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeProtoCopyTo(b *testing.B) {
	buf := bytes.NewBuffer(make([]byte, 5*1024*1024))
	l := len(msgs)
	msg := &pb.RobustMessage{
		Id:      &pb.RobustId{},
		Session: &pb.RobustId{},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgs[i%l].CopyToProtoMessage(msg)
		m, err := proto.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := buf.Write(m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeProtoBuffer(b *testing.B) {
	buf := bytes.NewBuffer(make([]byte, 5*1024*1024))
	l := len(msgs)
	pbuf := proto.NewBuffer(nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pbuf.Reset()
		if err := pbuf.Marshal(msgs[i%l].ProtoMessage()); err != nil {
			b.Fatal(err)
		}
		if _, err := buf.Write(pbuf.Bytes()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeProtoBufferCopyTo(b *testing.B) {
	buf := bytes.NewBuffer(make([]byte, 5*1024*1024))
	l := len(msgs)
	pbuf := proto.NewBuffer(nil)
	msg := &pb.RobustMessage{
		Id:      &pb.RobustId{},
		Session: &pb.RobustId{},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pbuf.Reset()
		msgs[i%l].CopyToProtoMessage(msg)
		if err := pbuf.Marshal(msg); err != nil {
			b.Fatal(err)
		}
		if _, err := buf.Write(pbuf.Bytes()); err != nil {
			b.Fatal(err)
		}
	}
}
