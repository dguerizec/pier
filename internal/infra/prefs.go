package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Prefs holds per-user pier preferences that are not tied to a specific
// project. Stored at <Paths.Root>/prefs.toml so they survive uninstalls
// of the local infra (traefik/dnsmasq) and stay independent of the
// install state in config.toml.
//
// The file is optional: a missing prefs.toml is not an error; LoadPrefs
// returns a zero-valued struct so callers can read defaults
// transparently.
type Prefs struct {
	// WorktreeDir is the per-user default location new worktrees land in
	// when `pier worktree add <name>` is given a bare name. Resolution
	// order at use time: .pier.local.toml [worktree].dir > .pier.toml
	// [worktree].dir > prefs.toml worktree_dir > hard-coded default.
	WorktreeDir string `toml:"worktree_dir,omitempty"`
}

// PrefsPath returns the canonical prefs.toml location.
func PrefsPath(p *Paths) string {
	return filepath.Join(p.Root, "prefs.toml")
}

// LoadPrefs reads <paths.Root>/prefs.toml. When the file does not exist
// the returned Prefs is zero-valued and the error is nil — pier init
// runs before any prefs are written, and that should not be an error.
func LoadPrefs(p *Paths) (*Prefs, error) {
	pr := &Prefs{}
	path := PrefsPath(p)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return pr, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := toml.DecodeFile(path, pr); err != nil {
		return nil, fmt.Errorf("infra: parse %s: %w", path, err)
	}
	return pr, nil
}

// Save writes pr to prefs.toml, creating the parent directory when
// missing. Existing fields not represented in pr are dropped — callers
// who want to merge should LoadPrefs first and mutate.
func (pr *Prefs) Save(p *Paths) error {
	if err := os.MkdirAll(p.Root, 0o755); err != nil {
		return err
	}
	path := PrefsPath(p)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(pr); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
