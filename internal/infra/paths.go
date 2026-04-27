package infra

import (
	"os"
	"path/filepath"
)

// Paths holds the on-disk locations pier owns under $XDG_CONFIG_HOME/pier.
type Paths struct {
	Root           string // ~/.config/pier
	ConfigFile     string // <Root>/config.toml — install state
	TraefikDir     string // <Root>/traefik
	TraefikStatic  string // <Root>/traefik/traefik.yml
	TraefikDynamic string // <Root>/traefik/dynamic — file provider entries
	DnsmasqDir     string // <Root>/dnsmasq
	DnsmasqConf    string // <Root>/dnsmasq/dnsmasq.conf
	StateDB        string // <Root>/state.db — SQLite
}

// DefaultPaths resolves the canonical layout. $XDG_CONFIG_HOME wins over
// ~/.config when set.
func DefaultPaths() (*Paths, error) {
	base, err := configHome()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "pier")
	return &Paths{
		Root:           root,
		ConfigFile:     filepath.Join(root, "config.toml"),
		TraefikDir:     filepath.Join(root, "traefik"),
		TraefikStatic:  filepath.Join(root, "traefik", "traefik.yml"),
		TraefikDynamic: filepath.Join(root, "traefik", "dynamic"),
		DnsmasqDir:     filepath.Join(root, "dnsmasq"),
		DnsmasqConf:    filepath.Join(root, "dnsmasq", "dnsmasq.conf"),
		StateDB:        filepath.Join(root, "state.db"),
	}, nil
}

// EnsureDirs creates the directory skeleton (idempotent).
func (p *Paths) EnsureDirs() error {
	for _, d := range []string{p.Root, p.TraefikDir, p.TraefikDynamic, p.DnsmasqDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func configHome() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}
