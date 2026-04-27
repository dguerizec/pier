package infra

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the persisted install state. Written by Install, read by other
// commands that need to know the active TLD or mode.
type Config struct {
	Mode   string `toml:"mode"`    // local | server
	TLD    string `toml:"tld"`     // base TLD (e.g. test)
	BindIP string `toml:"bind_ip"` // 127.0.0.1 in local mode, 0.0.0.0 in server
}

const (
	ModeLocal  = "local"
	ModeServer = "server"
)

// ErrNotInstalled means no config.toml exists at the expected location.
var ErrNotInstalled = errors.New("infra: pier is not installed (run `pier install`)")

// LoadConfig reads <paths.ConfigFile>.
func LoadConfig(p *Paths) (*Config, error) {
	if _, err := os.Stat(p.ConfigFile); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	c := &Config{}
	if _, err := toml.DecodeFile(p.ConfigFile, c); err != nil {
		return nil, fmt.Errorf("infra: parse %s: %w", p.ConfigFile, err)
	}
	return c, nil
}

// Save writes c to <paths.ConfigFile>.
func (c *Config) Save(p *Paths) error {
	f, err := os.Create(p.ConfigFile)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}
