// Package manifest parses, validates, and writes .pier.toml files
// described in DESIGN §5.4. Per-developer overrides in .pier.local.toml
// are merged on top of the project manifest.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

const (
	FileName       = ".pier.toml"
	LocalFileName  = ".pier.local.toml"
	KindCompose    = "compose"
	KindDockerfile = "dockerfile" // synthesized compose; adapter lands in Phase 3
)

// ErrNotFound signals that no .pier.toml exists at the given root.
var ErrNotFound = errors.New("manifest: .pier.toml not found (run `pier init`)")

// Manifest is the in-memory representation of .pier.toml.
type Manifest struct {
	Project     Project     `toml:"project"`
	Stack       Stack       `toml:"stack"`
	Materialize Materialize `toml:"materialize,omitempty"`
	Hooks       Hooks       `toml:"hooks,omitempty"`
	Watch       Watch       `toml:"watch,omitempty"`
	Worktree    Worktree    `toml:"worktree,omitempty"`
}

type Project struct {
	Name       string `toml:"name"`
	BaseDomain string `toml:"base_domain"`
}

// Stack covers the supported adapter kinds. Pier is intentionally
// docker-coupled: even one-off raw processes (uv/npm/cargo) are expected
// to declare a docker-compose.dev.yml. The README has a 10-line minimal
// example for projects that don't otherwise containerize.
type Stack struct {
	Kind       string `toml:"kind"`
	Port       int    `toml:"port"`
	File       string `toml:"file,omitempty"`       // compose
	Service    string `toml:"service,omitempty"`    // compose
	Dockerfile string `toml:"dockerfile,omitempty"` // dockerfile (Phase 3 — synthesized compose)

	// MatchHostUID, when true, makes pier inject `user: "<uid>:<gid>"`
	// into the compose override so the container runs as the host user.
	// Resolves the EACCES class on bind-mounted host paths when the image
	// uses a non-matching default user (typical for distroless/nonroot).
	MatchHostUID bool `toml:"match_host_uid,omitempty"`
}

type Materialize struct {
	Symlinks  []string `toml:"symlinks,omitempty"`
	Snapshots []string `toml:"snapshots,omitempty"`
}

type Hooks struct {
	PreUp    string `toml:"pre_up,omitempty"`
	PostUp   string `toml:"post_up,omitempty"`
	PreDown  string `toml:"pre_down,omitempty"`
	PostDown string `toml:"post_down,omitempty"`
}

type Watch struct {
	Paths    []string `toml:"paths,omitempty"`
	OnChange string   `toml:"on_change,omitempty"` // rebuild | restart
}

// Worktree configures where `pier worktree add <name>` places new trees
// when <name> has no path separator. Relative paths resolve against the
// primary worktree.
type Worktree struct {
	Dir string `toml:"dir,omitempty"` // e.g. ".claude/worktrees"
}

// Load reads <root>/.pier.toml, then layers <root>/.pier.local.toml on top
// if present, and validates the result.
func Load(root string) (*Manifest, error) {
	mainPath := filepath.Join(root, FileName)
	if _, err := os.Stat(mainPath); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}

	m := &Manifest{}
	if _, err := toml.DecodeFile(mainPath, m); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", mainPath, err)
	}

	localPath := filepath.Join(root, LocalFileName)
	if _, err := os.Stat(localPath); err == nil {
		if _, err := toml.DecodeFile(localPath, m); err != nil {
			return nil, fmt.Errorf("manifest: parse %s: %w", localPath, err)
		}
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Write serializes m as TOML to path. Overwrites any existing file.
func (m *Manifest) Write(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(m)
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Validate checks that required fields are set and consistent with Stack.Kind.
func (m *Manifest) Validate() error {
	if m.Project.Name == "" {
		return errors.New("manifest: project.name is required")
	}
	if !dnsLabel.MatchString(m.Project.Name) {
		return fmt.Errorf("manifest: project.name %q is not a valid DNS label", m.Project.Name)
	}
	if m.Project.BaseDomain == "" {
		return errors.New("manifest: project.base_domain is required")
	}

	switch m.Stack.Kind {
	case KindCompose:
		if m.Stack.File == "" {
			return errors.New("manifest: stack.file is required for kind=compose")
		}
		if m.Stack.Port == 0 {
			return errors.New("manifest: stack.port is required for kind=compose")
		}
	case KindDockerfile:
		if m.Stack.Dockerfile == "" {
			return errors.New("manifest: stack.dockerfile is required for kind=dockerfile")
		}
		if m.Stack.Port == 0 {
			return errors.New("manifest: stack.port is required for kind=dockerfile")
		}
	case "":
		return errors.New("manifest: stack.kind is required")
	default:
		return fmt.Errorf("manifest: stack.kind %q must be compose (or dockerfile, Phase 3)", m.Stack.Kind)
	}

	if oc := m.Watch.OnChange; oc != "" && oc != "rebuild" && oc != "restart" {
		return fmt.Errorf("manifest: watch.on_change %q must be rebuild or restart", oc)
	}
	return nil
}
