// Package applied stores the last workload configuration successfully applied
// to a worktree. It is teardown state, not desired configuration: pier down
// reads it even when the current manifest has changed or no longer parses.
package applied

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/manifest"
)

const (
	version  = 1
	stateDir = ".pier/applied"
	stateExt = ".json"
)

var ErrNotFound = errors.New("applied state not found")

// State is the effective workload contract recorded after adapter Apply. The
// file is mode 0600 because resolved manifest/environment values may be local
// secrets. AdapterData is deliberately secret-free and sufficient for teardown.
type State struct {
	Version        int               `json:"version"`
	Project        string            `json:"project"`
	Slug           string            `json:"slug"`
	WorktreePath   string            `json:"worktree_path"`
	PrimaryPath    string            `json:"primary_path,omitempty"`
	Branch         string            `json:"branch,omitempty"`
	BaseDomain     string            `json:"base_domain"`
	TLD            string            `json:"tld,omitempty"`
	DefaultService string            `json:"default_service,omitempty"`
	TraefikNetwork string            `json:"traefik_network"`
	ComposeEnv     map[string]string `json:"compose_env,omitempty"`
	Manifest       manifest.Manifest `json:"manifest"`
	AdapterData    []byte            `json:"adapter_data"`
	OverrideData   []byte            `json:"override_data,omitempty"`
	ContainerID    string            `json:"container_id,omitempty"`
	AppliedAt      time.Time         `json:"applied_at"`
}

func (s *State) Validate() error {
	if s.Version != version {
		return fmt.Errorf("applied state version %d is unsupported", s.Version)
	}
	if s.Project == "" || s.Slug == "" || s.WorktreePath == "" {
		return errors.New("applied state requires project, slug, and worktree_path")
	}
	if s.Manifest.Stack.Kind == "" {
		return errors.New("applied state requires adapter kind")
	}
	if len(s.AdapterData) == 0 {
		return errors.New("applied state requires adapter_data")
	}
	return nil
}

// Context reconstructs the adapter inputs used by the applied workload. The
// source stack file may since have changed; DownApplied consumes AdapterData
// instead and uses this context only for identity, cwd, env, and output.
func (s *State) Context(out, errOut io.Writer) adapter.Ctx {
	return adapter.Ctx{
		Project:        s.Project,
		Slug:           s.Slug,
		BaseDomain:     s.BaseDomain,
		TLD:            s.TLD,
		WorktreePath:   s.WorktreePath,
		Stack:          s.Manifest.Stack,
		Expose:         s.Manifest.Expose,
		Service:        s.Manifest.Service,
		DefaultService: s.DefaultService,
		Env:            s.Manifest.Env,
		ComposeEnv:     cloneMap(s.ComposeEnv),
		TraefikNetwork: s.TraefikNetwork,
		Out:            out,
		Err:            errOut,
	}
}

func (s *State) SameIdentity(c adapter.Ctx) bool {
	return s.Project == c.Project &&
		s.Slug == c.Slug &&
		s.Manifest.Stack.Kind == c.Stack.Kind &&
		s.Manifest.Stack.File == c.Stack.File
}

func New(c adapter.Ctx, primaryPath, branch string, m *manifest.Manifest, prepared *adapter.Prepared, handle *adapter.Handle) *State {
	state := &State{
		Version:        version,
		Project:        c.Project,
		Slug:           c.Slug,
		WorktreePath:   c.WorktreePath,
		PrimaryPath:    primaryPath,
		Branch:         branch,
		BaseDomain:     c.BaseDomain,
		TLD:            c.TLD,
		DefaultService: c.DefaultService,
		TraefikNetwork: c.TraefikNetwork,
		ComposeEnv:     cloneMap(c.ComposeEnv),
		Manifest:       *m,
		AppliedAt:      time.Now().UTC(),
	}
	if prepared != nil {
		state.AdapterData = append([]byte(nil), prepared.AdapterData...)
		state.OverrideData = append([]byte(nil), prepared.OverrideData...)
	}
	if handle != nil {
		state.ContainerID = handle.ContainerID
	}
	return state
}

func Path(worktreePath, slug string) string {
	return filepath.Join(worktreePath, stateDir, filepath.Base(slug)+stateExt)
}

func Load(worktreePath, slug string) (*State, error) {
	path := Path(worktreePath, slug)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read applied state %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("decode applied state %s: %w", path, err)
	}
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("validate applied state %s: %w", path, err)
	}
	if state.Slug != slug {
		return nil, fmt.Errorf("validate applied state %s: file slug %q does not match applied slug %q", path, slug, state.Slug)
	}
	return &state, nil
}

// List returns every applied workload stored in a worktree. It lets an
// implicit `pier down` retain the previously applied slug after a branch rename.
func List(worktreePath string) ([]*State, error) {
	dir := filepath.Join(worktreePath, stateDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read applied state directory %s: %w", dir, err)
	}
	states := make([]*State, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != stateExt {
			continue
		}
		slug := entry.Name()[:len(entry.Name())-len(stateExt)]
		state, err := Load(worktreePath, slug)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Slug < states[j].Slug })
	return states, nil
}

func Save(state *State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	path := Path(state.WorktreePath, state.Slug)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir applied state: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod applied state directory: %w", err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode applied state: %w", err)
	}
	body = append(body, '\n')
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create applied state: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return fmt.Errorf("write applied state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync applied state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close applied state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace applied state: %w", err)
	}
	return nil
}

func Delete(worktreePath, slug string) error {
	path := Path(worktreePath, slug)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove applied state %s: %w", path, err)
	}
	_ = os.Remove(filepath.Dir(path))
	return nil
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
