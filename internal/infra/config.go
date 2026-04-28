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
	Mode   string `toml:"mode"`              // local | server
	TLD    string `toml:"tld"`               // base TLD (e.g. test)
	BindIP string `toml:"bind_ip"`           // listen IP — 127.0.0.1 (local) | 0.0.0.0 or specific (server)
	// AnswerIP is what dnsmasq returns as the A record for *.tld. Equal to
	// BindIP in local mode; set to the reachable IP (typically tailnet) in
	// server mode so peers know where to send HTTP traffic.
	AnswerIP string `toml:"answer_ip,omitempty"`

	// TraefikNetwork is the docker network workloads register on for traefik
	// label discovery. Defaults to NetworkName ("pier") in standard mode;
	// overridden to the user's existing network in BYO-traefik mode.
	TraefikNetwork string `toml:"traefik_network,omitempty"`
	// ExternalTraefik names the user-managed traefik container in BYO mode.
	// Empty means pier owns its own pier-traefik container.
	ExternalTraefik string `toml:"external_traefik,omitempty"`
}

// EffectiveAnswerIP returns AnswerIP or BindIP (older configs written before
// AnswerIP existed used BindIP for both purposes).
func (c *Config) EffectiveAnswerIP() string {
	if c.AnswerIP != "" {
		return c.AnswerIP
	}
	return c.BindIP
}

// EffectiveTraefikNetwork returns TraefikNetwork or NetworkName when unset
// (older configs written before the field existed).
func (c *Config) EffectiveTraefikNetwork() string {
	if c.TraefikNetwork != "" {
		return c.TraefikNetwork
	}
	return NetworkName
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
