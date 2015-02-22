package config

import (
	"time"

	"github.com/BurntSushi/toml"
)

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
	Revision       int `toml:"-"`
	IRC            IRC
	SessionTimeout time.Duration
}

var DefaultConfig = Network{
	SessionTimeout: 30 * time.Second,
}

func FromString(input string) (Network, error) {
	var cfg Network
	_, err := toml.Decode(input, &cfg)
	return cfg, err
}
