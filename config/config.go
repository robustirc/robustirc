package config

import (
	"time"

	"github.com/BurntSushi/toml"
)

type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err == nil {
		*d = Duration(parsed)
	}
	return err
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d Duration) String() string {
	return time.Duration(d).String()
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
	Revision uint64 `toml:"-"`

	IRC IRC

	// Time interval after which a session without any activity is terminated
	// by the server. The client should send a PING every minute.
	SessionExpiration Duration

	// Enforced cooloff between two messages sent by a user. Set to 0 to disable throttling.
	PostMessageCooloff Duration

	// TrustedBridges is a map from X-Bridge-Auth header to human-readable
	// name. For all bridges which send a configured header, the
	// X-Forwarded-For header is respected.
	TrustedBridges map[string]string
}

var DefaultConfig = Network{
	SessionExpiration:  Duration(30 * time.Minute),
	PostMessageCooloff: Duration(500 * time.Millisecond),
}

func FromString(input string) (Network, error) {
	var cfg Network
	_, err := toml.Decode(input, &cfg)
	// TODO(secure): Use scrypt to hash the ircop passwords to make brute-forcing harder.
	return cfg, err
}
