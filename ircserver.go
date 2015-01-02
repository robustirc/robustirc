// ircserver is the entry point for all IRC-related logic.
package main

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"fancyirc/types"

	"github.com/sorcix/irc"
)

var (
	sessionMu sync.Mutex
	sessions  = make(map[types.FancyId]*Session)

	ircOutput  []types.FancyMessage
	newMessage = sync.NewCond(&sync.Mutex{})
)

type Session struct {
	Id       types.FancyId
	Nick     string
	Channels map[string]bool
}

func (s *Session) ircPrefix() *irc.Prefix {
	// TODO(secure): Is there a better value for User?
	return &irc.Prefix{
		Name: s.Nick,
		User: "fancy",
		Host: fmt.Sprintf("fancy/0x%x", s.Id),
	}
}

func (s *Session) interestedIn(msg *types.FancyMessage) bool {
	log.Printf("[DEBUG] Checking whether %+v is interested in %+v\n", s, msg)
	ircmsg := irc.ParseMessage(msg.Data)
	serverPrefix := irc.Prefix{Name: *network}

	if *ircmsg.Prefix == serverPrefix && msg.Session == s.Id {
		return true
	}

	switch ircmsg.Command {
	case irc.NICK:
		// TODO(secure): does it make sense to restrict this to sessions which
		// have a channel in common? noting this because it doesn’t handle the
		// query-only use-case. if there’s no downside (except for the privacy
		// aspect), perhaps leave it as-is?
		return true
	case irc.JOIN:
		return s.Channels[ircmsg.Trailing]
	case irc.PART:
		return *s.ircPrefix() == *ircmsg.Prefix || s.Channels[ircmsg.Params[0]]
	case irc.PRIVMSG:
		return *s.ircPrefix() != *ircmsg.Prefix && (s.Channels[ircmsg.Params[0]] || ircmsg.Params[0] == s.Nick)
	case irc.MODE:
		return ircmsg.Params[0] == s.Nick || s.Channels[ircmsg.Params[0]]
	default:
		return false
	}
}

// processMessage modifies state in response to 'message' and returns zero or
// more IRC messages in response to 'message'.
func processMessage(session types.FancyId, message *irc.Message) {
	var replies []irc.Message

	// alias for convenience
	s := sessions[session]

	switch message.Command {
	case irc.NICK:
		oldPrefix := s.ircPrefix()
		s.Nick = message.Params[0]
		// TODO(secure): when connecting, handle nickname already in use.
		// :ridcully.twice-irc.de 433 * secure :Nickname is already in use.
		// TODO(secure): handle nickname already in use.
		log.Printf("nickname now: %+v\n", s)
		if !strings.HasPrefix(oldPrefix.String(), "!") {
			replies = append(replies, irc.Message{
				Prefix:   oldPrefix,
				Command:  irc.NICK,
				Trailing: message.Params[0],
			})
		}

	case irc.USER:
		// TODO(secure): send 002, 003, 004, 005, 251, 252, 254, 255, 265, 266, [motd = 375, 372, 376]
		replies = append(replies, irc.Message{
			Prefix:   &irc.Prefix{Name: *network},
			Command:  irc.RPL_WELCOME,
			Params:   []string{s.Nick},
			Trailing: "Welcome to fancyirc :)",
		})

	case irc.JOIN:
		// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
		channel := message.Params[0]
		s.Channels[channel] = true
		var nicks []string
		// TODO(secure): a separate map for quick lookup may be worthwhile for big channels.
		for _, session := range sessions {
			if !session.Channels[channel] {
				continue
			}
			nicks = append(nicks, session.Nick)
		}
		replies = append(replies, irc.Message{
			Prefix:   s.ircPrefix(),
			Command:  irc.JOIN,
			Trailing: channel,
		})
		//replies = append(replies, irc.Message{
		//	Prefix:  &irc.Prefix{Name: *network},
		//	Command: irc.RPL_NOTOPIC,
		//	Params:  []string{channel},
		//})
		// TODO(secure): why the = param?
		replies = append(replies, irc.Message{
			Prefix:   &irc.Prefix{Name: *network},
			Command:  irc.RPL_NAMREPLY,
			Params:   []string{s.Nick, "=", channel},
			Trailing: strings.Join(nicks, " "),
		})
		replies = append(replies, irc.Message{
			Prefix:   &irc.Prefix{Name: *network},
			Command:  irc.RPL_ENDOFNAMES,
			Params:   []string{s.Nick, channel},
			Trailing: "End of /NAMES list.",
		})

	case irc.PART:
		// TODO(secure): strictly speaking, RFC1459 says one can join multiple channels at once.
		channel := message.Params[0]
		s.Channels[channel] = false
		replies = append(replies, irc.Message{
			Prefix:  s.ircPrefix(),
			Command: irc.PART,
			Params:  []string{channel},
		})

	case irc.PRIVMSG:
		replies = append(replies, irc.Message{
			Prefix:   s.ircPrefix(),
			Command:  irc.PRIVMSG,
			Params:   []string{message.Params[0]},
			Trailing: message.Trailing,
		})

	case irc.MODE:
		channel := message.Params[0]
		// TODO(secure): properly distinguish between users and channels
		if s.Channels[channel] {
			if len(message.Params) > 1 && message.Params[1] == "b" {
				replies = append(replies, irc.Message{
					Prefix:   &irc.Prefix{Name: *network},
					Command:  irc.RPL_ENDOFBANLIST,
					Params:   []string{s.Nick, channel},
					Trailing: "End of Channel Ban List",
				})
			} else {
				replies = append(replies, irc.Message{
					Prefix:  &irc.Prefix{Name: *network},
					Command: irc.RPL_CHANNELMODEIS,
					Params:  []string{s.Nick, channel, "+"},
				})
			}
		} else {
			if channel == s.Nick {
				replies = append(replies, irc.Message{
					Prefix:   s.ircPrefix(),
					Command:  irc.MODE,
					Params:   []string{s.Nick},
					Trailing: "+",
				})
			} else {
				replies = append(replies, irc.Message{
					Prefix:   &irc.Prefix{Name: *network},
					Command:  irc.ERR_NOTONCHANNEL,
					Params:   []string{s.Nick, channel},
					Trailing: "You're not on that channel",
				})
			}
		}

	case irc.WHO:
		channel := message.Params[0]
		// TODO(secure): a separate map for quick lookup may be worthwhile for big channels.
		for _, session := range sessions {
			if !session.Channels[channel] {
				continue
			}
			prefix := session.ircPrefix()
			replies = append(replies, irc.Message{
				Prefix:   &irc.Prefix{Name: *network},
				Command:  irc.RPL_WHOREPLY,
				Params:   []string{s.Nick, channel, prefix.User, prefix.Host, *network, prefix.Name, "H"},
				Trailing: "0 Unknown",
			})
		}

		replies = append(replies, irc.Message{
			Prefix:   &irc.Prefix{Name: *network},
			Command:  irc.RPL_ENDOFWHO,
			Params:   []string{s.Nick, channel},
			Trailing: "End of /WHO list",
		})
	}

	for _, reply := range replies {
		fancymsg := types.NewFancyMessage(types.FancyIRCToClient, session, string(reply.Bytes()))
		ircOutput = append(ircOutput, *fancymsg)
	}

	if len(replies) > 0 {
		newMessage.Broadcast()
	}
}

// GetMessage returns the IRC message with index 'idx', possibly blocking until
// that message appears.
func GetMessage(idx int) *types.FancyMessage {
	newMessage.L.Lock()
	// Sleep until processMessage() wakes us up for a new message.
	for idx >= len(ircOutput) {
		newMessage.Wait()
	}
	newMessage.L.Unlock()

	return &ircOutput[idx]
}
