package ircserver

import (
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/types"
	"gopkg.in/sorcix/irc.v2"

	pb "github.com/robustirc/robustirc/proto"
)

func timeToTimestamp(t time.Time) *pb.Timestamp {
	return &pb.Timestamp{
		UnixNano: t.UnixNano(),
		IsZero:   t.IsZero(),
	}
}

func timestampToTime(t *pb.Timestamp) time.Time {
	if t == nil || t.IsZero {
		return time.Time{}
	}
	return time.Unix(0, t.UnixNano)
}

func (i *IRCServer) Marshal(lastIncludedIndex uint64) ([]byte, error) {
	i.sessionsMu.RLock()
	defer i.sessionsMu.RUnlock()
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	sessions := make([]*pb.Snapshot_Session, 0, len(i.sessions))
	for id, session := range i.sessions {
		channels := make([]string, 0, len(session.Channels))
		for channel, _ := range session.Channels {
			channels = append(channels, string(channel))
		}
		invitedTo := make([]string, 0, len(session.invitedTo))
		for channel, _ := range session.invitedTo {
			invitedTo = append(invitedTo, string(channel))
		}
		modes := make([]string, 0)
		for mode := 'A'; mode < 'z'; mode++ {
			if session.modes[mode] {
				modes = append(modes, string(mode))
			}
		}
		sessions = append(sessions, &pb.Snapshot_Session{
			Id:                 &pb.RobustId{Id: id.Id, Reply: id.Reply},
			Auth:               session.auth,
			Nick:               session.Nick,
			Username:           session.Username,
			Realname:           session.Realname,
			Channels:           channels,
			LastActivity:       timeToTimestamp(session.LastActivity),
			LastNonPing:        timeToTimestamp(session.LastNonPing),
			Operator:           session.Operator,
			AwayMsg:            session.AwayMsg,
			ThrottlingExponent: int64(session.throttlingExponent),
			InvitedTo:          invitedTo,
			Modes:              modes,
			Svid:               session.svid,
			Pass:               session.Pass,
			Server:             session.Server,
			StartId: &pb.RobustId{
				Id:    session.startId.Id,
				Reply: session.startId.Reply,
			},
			LastClientMessageId: session.lastClientMessageId,
			IrcPrefix: &pb.Snapshot_IRCPrefix{
				Name: session.ircPrefix.Name,
				User: session.ircPrefix.User,
				Host: session.ircPrefix.Host,
			},
		})
	}

	channels := make([]*pb.Snapshot_Channel, 0, len(i.channels))
	for _, channel := range i.channels {
		nicks := make(map[string]*pb.Snapshot_Channel_Modes, len(channel.nicks))
		for nickName, channelNickModes := range channel.nicks {
			var modes []string
			// channelNickModes goes from chanop to maxChanMemberStatus.
			for mode, value := range channelNickModes {
				if value {
					modes = append(modes, string(mode))
				}
			}
			nicks[string(nickName)] = &pb.Snapshot_Channel_Modes{Mode: modes}
		}
		var modes []string
		for mode := 'A'; mode < 'z'; mode++ {
			if channel.modes[mode] {
				modes = append(modes, string(mode))
			}
		}
		channels = append(channels, &pb.Snapshot_Channel{
			Name:      channel.name,
			TopicNick: channel.topicNick,
			TopicTime: timeToTimestamp(channel.topicTime),
			Topic:     channel.topic,
			Nicks:     nicks,
			Modes:     modes,
		})
	}

	svsholds := make(map[string]*pb.Snapshot_SVSHold, len(i.svsholds))
	for nickName, svshold := range i.svsholds {
		svsholds[string(nickName)] = &pb.Snapshot_SVSHold{
			Added:    timeToTimestamp(svshold.added),
			Duration: svshold.duration.String(),
			Reason:   svshold.reason,
		}
	}
	operators := make([]*pb.Snapshot_Config_IRC_Operator, 0, len(i.Config.IRC.Operators))
	for _, ircop := range i.Config.IRC.Operators {
		operators = append(operators, &pb.Snapshot_Config_IRC_Operator{
			Name:     ircop.Name,
			Password: ircop.Password,
		})
	}
	services := make([]*pb.Snapshot_Config_IRC_Service, 0, len(i.Config.IRC.Services))
	for _, service := range i.Config.IRC.Services {
		services = append(services, &pb.Snapshot_Config_IRC_Service{
			Password: service.Password,
		})
	}
	config := &pb.Snapshot_Config{
		Revision: i.Config.Revision,
		Irc: &pb.Snapshot_Config_IRC{
			Operators: operators,
			Services:  services,
		},
		SessionExpiration:  i.Config.SessionExpiration.String(),
		PostMessageCooloff: i.Config.PostMessageCooloff.String(),
	}
	snapshot := pb.Snapshot{
		Sessions:          sessions,
		Channels:          channels,
		Svsholds:          svsholds,
		LastProcessed:     &pb.RobustId{Id: i.lastProcessed.Id, Reply: i.lastProcessed.Reply},
		Config:            config,
		LastIncludedIndex: lastIncludedIndex,
	}
	return proto.Marshal(&snapshot)
}

// Unmarshal treats |data| as a protobuf-encoded snapshot of IRCServer
// state and applies it to the IRCServer. It returns the last included
// ircstore index of the snapshot.
func (i *IRCServer) Unmarshal(data []byte) (uint64, error) {
	var snapshot pb.Snapshot
	if err := proto.Unmarshal(data, &snapshot); err != nil {
		return 0, err
	}

	for _, s := range snapshot.Sessions {
		channels := make(map[lcChan]bool, len(s.Channels))
		for _, channel := range s.Channels {
			channels[ChanToLower(channel)] = true
		}
		invitedTo := make(map[lcChan]bool, len(s.InvitedTo))
		for _, channel := range s.InvitedTo {
			invitedTo[ChanToLower(channel)] = true
		}
		var modes ['z']bool
		for _, mode := range s.Modes {
			modes[mode[0]] = true
		}
		newSession := &Session{
			Id:                 types.RobustId{Id: s.Id.Id, Reply: s.Id.Reply},
			auth:               s.Auth,
			Nick:               s.Nick,
			Username:           s.Username,
			Realname:           s.Realname,
			Channels:           channels,
			LastActivity:       timestampToTime(s.LastActivity),
			LastNonPing:        timestampToTime(s.LastNonPing),
			Operator:           s.Operator,
			AwayMsg:            s.AwayMsg,
			throttlingExponent: int(s.ThrottlingExponent),
			invitedTo:          invitedTo,
			modes:              modes,
			svid:               s.Svid,
			Pass:               s.Pass,
			Server:             s.Server,
			startId: types.RobustId{
				Id:    s.StartId.Id,
				Reply: s.StartId.Reply,
			},
			lastClientMessageId: s.LastClientMessageId,
			ircPrefix: irc.Prefix{
				Name: s.IrcPrefix.Name,
				User: s.IrcPrefix.User,
				Host: s.IrcPrefix.Host,
			},
		}
		if newSession.LastNonPing.IsZero() {
			newSession.LastNonPing = newSession.LastActivity
		}
		i.sessions[newSession.Id] = newSession
		if s.Server {
			i.serverSessions = append(i.serverSessions, newSession.Id.Id)
		}
		i.nicks[NickToLower(newSession.Nick)] = newSession
	}
	for _, c := range snapshot.Channels {
		nicks := make(map[lcNick]*[maxChanMemberStatus]bool, len(c.Nicks))
		for nickName, channelNickModes := range c.Nicks {
			var modes [maxChanMemberStatus]bool
			for _, mode := range channelNickModes.Mode {
				modes[mode[0]] = true
			}
			nicks[NickToLower(nickName)] = &modes
		}
		var modes ['z']bool
		for _, mode := range c.Modes {
			modes[mode[0]] = true
		}
		newChannel := channel{
			name:      c.Name,
			topicNick: c.TopicNick,
			topicTime: timestampToTime(c.TopicTime),
			topic:     c.Topic,
			nicks:     nicks,
			modes:     modes,
		}
		i.channels[ChanToLower(newChannel.name)] = &newChannel
	}
	for nickName, s := range snapshot.Svsholds {
		duration, err := time.ParseDuration(s.Duration)
		if err != nil {
			return 0, err
		}
		i.svsholds[NickToLower(nickName)] = svshold{
			added:    timestampToTime(s.Added),
			duration: duration,
			reason:   s.Reason,
		}
	}
	i.lastProcessed = types.RobustId{
		Id:    snapshot.LastProcessed.Id,
		Reply: snapshot.LastProcessed.Reply,
	}
	operators := make([]config.IRCOp, len(snapshot.Config.Irc.Operators))
	for idx, operator := range snapshot.Config.Irc.Operators {
		operators[idx] = config.IRCOp{
			Name:     operator.Name,
			Password: operator.Password,
		}
	}
	services := make([]config.Service, len(snapshot.Config.Irc.Services))
	for idx, service := range snapshot.Config.Irc.Services {
		services[idx] = config.Service{
			Password: service.Password,
		}
	}
	sessionExpiration, err := time.ParseDuration(snapshot.Config.SessionExpiration)
	if err != nil {
		return 0, err
	}
	postMessageCooloff, err := time.ParseDuration(snapshot.Config.PostMessageCooloff)
	if err != nil {
		return 0, err
	}
	i.Config = config.Network{
		Revision: snapshot.Config.Revision,
		IRC: config.IRC{
			Operators: operators,
			Services:  services,
		},
		SessionExpiration:  config.Duration(sessionExpiration),
		PostMessageCooloff: config.Duration(postMessageCooloff),
	}

	return snapshot.LastIncludedIndex, nil
}
