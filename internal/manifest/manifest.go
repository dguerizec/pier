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
	KindProcess    = "process"
	KindDockerfile = "dockerfile"
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
}

type Project struct {
	Name       string `toml:"name"`
	BaseDomain string `toml:"base_domain"`
}

// Stack covers all three adapter kinds. Only fields relevant to Kind are
// expected to be set; Validate enforces this.
type Stack struct {
	Kind       string `toml:"kind"`
	Port       int    `toml:"port"`
	File       string `toml:"file,omitempty"`        // compose
	Service    string `toml:"service,omitempty"`     // compose
	Cmd        string `toml:"cmd,omitempty"`         // process
	PortEnv    string `toml:"port_env,omitempty"`    // process
	Dockerfile string `toml:"dockerfile,omitempty"`  // dockerfile
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
	case KindProcess:
		if m.Stack.Cmd == "" {
			return errors.New("manifest: stack.cmd is required for kind=process")
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
		return fmt.Errorf("manifest: stack.kind %q must be one of compose|process|dockerfile", m.Stack.Kind)
	}

	if oc := m.Watch.OnChange; oc != "" && oc != "rebuild" && oc != "restart" {
		return fmt.Errorf("manifest: watch.on_change %q must be rebuild or restart", oc)
	}
	return nil
}
