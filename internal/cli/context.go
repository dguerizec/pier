package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	sluglib "github.com/LeoPartt/pier/internal/slug"
	"github.com/LeoPartt/pier/internal/state"
	"github.com/LeoPartt/pier/internal/worktree"
)

// daily is the bundle of context most everyday commands need: who am I,
// where am I, what's the manifest, what's the slug, plus a state handle.
type daily struct {
	Worktree *worktree.Info
	Manifest *manifest.Manifest
	Slug     string
	Ctx      adapter.Ctx
	State    *state.Store
	Paths    *infra.Paths
}

// resolveDaily detects the worktree, loads the manifest, computes the slug
// (PIER_SLUG env or --slug flag override the branch derivation), and opens
// the state DB. Caller MUST defer d.State.Close() on success.
func resolveDaily(slugOverride string) (*daily, error) {
	info, err := worktree.Detect()
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(info.Toplevel)
	if err != nil {
		return nil, err
	}

	slug := slugOverride
	if slug == "" {
		slug = os.Getenv("PIER_SLUG")
	}
	if slug == "" {
		slug, err = sluglib.FromBranch(info.Branch)
		if err != nil {
			return nil, fmt.Errorf("derive slug from branch %q: %w", info.Branch, err)
		}
	} else if err := sluglib.Validate(slug); err != nil {
		return nil, fmt.Errorf("invalid slug %q: %w", slug, err)
	}

	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil, err
	}
	if _, err := infra.LoadConfig(paths); errors.Is(err, infra.ErrNotInstalled) {
		return nil, fmt.Errorf("%w (hint: pier install)", err)
	} else if err != nil {
		return nil, err
	}
	if err := paths.EnsureDirs(); err != nil {
		return nil, err
	}

	store, err := state.Open(paths.StateDB)
	if err != nil {
		return nil, err
	}

	return &daily{
		Worktree: info,
		Manifest: m,
		Slug:     slug,
		Paths:    paths,
		State:    store,
		Ctx: adapter.Ctx{
			Project:      m.Project.Name,
			Slug:         slug,
			BaseDomain:   m.Project.BaseDomain,
			WorktreePath: info.Toplevel,
			Stack:        m.Stack,
			Out:          os.Stdout,
			Err:          os.Stderr,
		},
	}, nil
}
