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

	// HeadscaleContainer + HeadscaleRecordsPath enable the records adapter:
	// when set, every pier up/down appends/removes an A record in the
	// headscale extra_records JSON file so peers can resolve pier slugs
	// via MagicDNS even when the TLD lives under headscale's base_domain
	// (where split-DNS rules are pre-empted by MagicDNS authoritative
	// scope). Both fields are populated by the install wizard when
	// extra_records_path is detected.
	HeadscaleContainer   string `toml:"headscale_container,omitempty"`
	HeadscaleRecordsPath string `toml:"headscale_records_path,omitempty"`
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
