package config

import (
	"time"

	"github.com/BurntSushi/toml"
)

// TODO(secure): use a custom type with encoding.TextUnmarshaler for durations, see https://github.com/BurntSushi/toml

type IRCOp struct {
	Name     string
	Password string
}

// IRC is the IRC-related configuration.
type IRC struct {
	Operators []IRCOp
}

// Network is the network configuration, i.e. the top level.
type Network struct {
	Revision int `toml:"-"`

	IRC IRC

	// Time interval after which a session without any activity is terminated
	// by the server. The client should send a PING every minute.
	SessionExpiration time.Duration

	// Enforced cooloff between two messages sent by a user. Set to 0 to disable throttling.
	PostMessageCooloff time.Duration
}

var DefaultConfig = Network{
	SessionExpiration:  30 * time.Minute,
	PostMessageCooloff: 500 * time.Millisecond,
}

func FromString(input string) (Network, error) {
	var cfg Network
	_, err := toml.Decode(input, &cfg)
	// TODO(secure): Use scrypt to hash the ircop passwords to make brute-forcing harder.
	return cfg, err
}
