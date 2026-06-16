package initwizard

import "github.com/dguerizec/pier/internal/infra"

// InstalledTLD returns the TLD pier was installed with so the default
// base_domain is coherent with the host (e.g. `<name>.test`). Falls back to the
// hard-coded default when pier isn't installed yet — pier init shouldn't
// require pier install.
func InstalledTLD() string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return infra.DefaultTLD
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil || cfg.TLD == "" {
		return infra.DefaultTLD
	}
	return cfg.TLD
}
