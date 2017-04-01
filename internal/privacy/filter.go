// Package privacy provides functions for removing private information
// from data of different types.
package privacy

import (
	"github.com/golang/protobuf/proto"
	"gopkg.in/sorcix/irc.v2"

	pb "github.com/robustirc/robustirc/internal/proto"
	"github.com/robustirc/robustirc/internal/robust"
)

func FilterSnapshot(snapshot pb.Snapshot) pb.Snapshot {
	result := proto.Clone(&snapshot).(*pb.Snapshot)
	for _, session := range result.Sessions {
		session.Pass = "<privacy filtered>"
	}
	return *result
}

func FilterIrcmsg(message *irc.Message) *irc.Message {
	if message == nil {
		return nil
	}
	if message.Command == irc.PRIVMSG ||
		message.Command == irc.NOTICE ||
		message.Command == irc.PASS {
		if len(message.Params) > 0 {
			message.Params[len(message.Params)-1] = "<privacy filtered>"
		}
	}
	return message
}

func FilterMsg(message *robust.Message) *robust.Message {
	return &robust.Message{
		Id:      message.Id,
		Session: message.Session,
		Type:    message.Type,
		Data:    FilterIrcmsg(irc.ParseMessage(message.Data)).String(),
	}
}

func FilterMsgs(messages []*robust.Message) []*robust.Message {
	output := make([]*robust.Message, len(messages))
	for idx, message := range messages {
		output[idx] = FilterMsg(message)
	}
	return output
}
