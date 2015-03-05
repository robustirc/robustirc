package config

import (
	"time"

	"github.com/BurntSushi/toml"
)

type duration time.Duration

func (d *duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err == nil {
		*d = duration(parsed)
	}
	return err
}

func (d duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

type IRCOp struct {
	Name     string
	Password string
}

type Service struct {
	Password string
}

// IRC is the IRC-related configuration.
type IRC struct {
	Operators []IRCOp
	Services  []Service
}

// Network is the network configuration, i.e. the top level.
type Network struct {
	Revision int `toml:"-"`

	IRC IRC

	// Time interval after which a session without any activity is terminated
	// by the server. The client should send a PING every minute.
	SessionExpiration duration

	// Enforced cooloff between two messages sent by a user. Set to 0 to disable throttling.
	PostMessageCooloff duration
}

var DefaultConfig = Network{
	SessionExpiration:  duration(30 * time.Minute),
	PostMessageCooloff: duration(500 * time.Millisecond),
}

func FromString(input string) (Network, error) {
	var cfg Network
	_, err := toml.Decode(input, &cfg)
	// TODO(secure): Use scrypt to hash the ircop passwords to make brute-forcing harder.
	return cfg, err
}
