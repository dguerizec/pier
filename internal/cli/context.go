package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
	Config   *infra.Config
}

// resolveSlugInput accepts what the user typed for --slug / PIER_SLUG and
// returns the canonical slug. The value can be one of three things, tried in
// order:
//
//  1. an already-valid slug (DNS label) — used verbatim;
//  2. a branch name in this repo — derived via slug.FromBranch;
//  3. a worktree path or its basename — derived from that worktree's branch.
//
// Each form maps to the same canonical slug, so users can copy-paste whatever
// shape they have at hand (`feat/x`, `feat-x`, `worktrees/feat-x`, `feat-x`).
func resolveSlugInput(toplevel, value string) (string, error) {
	if err := sluglib.Validate(value); err == nil {
		return value, nil
	}
	if branchExists(toplevel, value) {
		return sluglib.FromBranch(value)
	}
	if branch, ok := worktreeBranch(toplevel, value); ok {
		return sluglib.FromBranch(branch)
	}
	return "", fmt.Errorf("--slug %q: not a valid slug, branch, or worktree", value)
}

// branchExists reports whether name is a local branch in the repo at toplevel.
func branchExists(toplevel, name string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = toplevel
	return cmd.Run() == nil
}

// worktreeBranch returns the branch of the worktree whose absolute path or
// basename matches value. Falls back to ("", false) when no match.
func worktreeBranch(toplevel, value string) (string, bool) {
	entries, err := worktree.List(toplevel)
	if err != nil {
		return "", false
	}
	abs, _ := filepath.Abs(value)
	for _, e := range entries {
		if e.Branch == "" {
			continue
		}
		if e.Path == value || e.Path == abs || filepath.Base(e.Path) == value {
			return e.Branch, true
		}
	}
	return "", false
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

	slugInput := slugOverride
	if slugInput == "" {
		slugInput = os.Getenv("PIER_SLUG")
	}
	var slug string
	if slugInput == "" {
		slug, err = sluglib.FromBranch(info.Branch)
		if err != nil {
			return nil, fmt.Errorf("derive slug from branch %q: %w", info.Branch, err)
		}
	} else {
		slug, err = resolveSlugInput(info.Toplevel, slugInput)
		if err != nil {
			return nil, err
		}
	}

	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil, err
	}
	cfg, err := infra.LoadConfig(paths)
	if errors.Is(err, infra.ErrNotInstalled) {
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
		Config:   cfg,
		State:    store,
		Ctx: adapter.Ctx{
			Project:        m.Project.Name,
			Slug:           slug,
			BaseDomain:     m.Project.BaseDomain,
			WorktreePath:   info.Toplevel,
			Stack:          m.Stack,
			TraefikNetwork: cfg.EffectiveTraefikNetwork(),
			Out:            os.Stdout,
			Err:            os.Stderr,
		},
	}, nil
}
