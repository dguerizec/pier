package cli

import (
	"errors"
	"fmt"
	"io"
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

// resolveTarget picks which worktree the daily command should operate on
// and returns that worktree's Info along with the canonical slug. With an
// empty input, the current cwd worktree wins. Otherwise we look across
// every registered worktree for a match against (in any order):
//
//   - the derived slug
//   - the branch name
//   - the worktree's absolute path
//   - the worktree's basename
//
// When a match is found we DetectFrom() that worktree's path so the Ctx
// reflects the right toplevel, branch, and primary — bind mounts and
// materialization need that to be correct, otherwise pier targets the
// current cwd worktree's filesystem with the other worktree's slug.
//
// When the input is a valid slug shape but matches no worktree, we keep
// the current worktree and use the literal slug. Lets `pier up --slug X`
// stay useful right after renaming a branch, without forcing the user to
// cd around.
func resolveTarget(current *worktree.Info, slugInput string) (*worktree.Info, string, error) {
	if slugInput == "" {
		slug, err := sluglib.FromBranch(current.Branch)
		if err != nil {
			return nil, "", fmt.Errorf("derive slug from branch %q: %w", current.Branch, err)
		}
		return current, slug, nil
	}

	entries, err := worktree.List(current.Toplevel)
	if err == nil {
		abs, _ := filepath.Abs(slugInput)
		for _, e := range entries {
			if e.Branch == "" {
				continue
			}
			derived, derivedErr := sluglib.FromBranch(e.Branch)
			matches := e.Branch == slugInput ||
				e.Path == slugInput ||
				e.Path == abs ||
				filepath.Base(e.Path) == slugInput ||
				(derivedErr == nil && derived == slugInput)
			if !matches {
				continue
			}
			if derivedErr != nil {
				return nil, "", fmt.Errorf("derive slug from branch %q: %w", e.Branch, derivedErr)
			}
			info, err := worktree.DetectFrom(e.Path)
			if err != nil {
				return nil, "", fmt.Errorf("detect worktree at %s: %w", e.Path, err)
			}
			return info, derived, nil
		}
	}

	if err := sluglib.Validate(slugInput); err == nil {
		return current, slugInput, nil
	}
	if branchExists(current.Toplevel, slugInput) {
		slug, err := sluglib.FromBranch(slugInput)
		if err != nil {
			return nil, "", err
		}
		return current, slug, nil
	}
	return nil, "", fmt.Errorf("--slug %q: not a valid slug, branch, or worktree", slugInput)
}

// branchExists reports whether name is a local branch in the repo at toplevel.
func branchExists(toplevel, name string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = toplevel
	return cmd.Run() == nil
}

// resolveDaily detects the worktree, loads the manifest, computes the slug
// (PIER_SLUG env or --slug flag override the branch derivation), and opens
// the state DB. When --slug points at a different worktree than cwd, the
// returned context targets that worktree's filesystem too — bind mounts
// and materialization need it. Caller MUST defer d.State.Close() on success.
func resolveDaily(slugOverride string) (*daily, error) {
	current, err := worktree.Detect()
	if err != nil {
		return nil, err
	}

	slugInput := slugOverride
	if slugInput == "" {
		slugInput = os.Getenv("PIER_SLUG")
	}
	info, slug, err := resolveTarget(current, slugInput)
	if err != nil {
		return nil, err
	}
	return dailyForWorktree(info, slug, os.Stdout, os.Stderr)
}

// dailyForWorktree builds a daily for a pre-resolved worktree info. Used
// by resolveDaily (via cwd) and the REST API (via state-DB path lookup).
// When slug is empty, derive it from the worktree's branch.
func dailyForWorktree(info *worktree.Info, slug string, out, errW io.Writer) (*daily, error) {
	if slug == "" {
		derived, err := sluglib.FromBranch(info.Branch)
		if err != nil {
			return nil, fmt.Errorf("derive slug from branch %q: %w", info.Branch, err)
		}
		slug = derived
	}

	m, err := manifest.Load(info.Toplevel)
	if err != nil {
		return nil, err
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

	defaultService := ""
	if d := m.DefaultExpose(); d != nil {
		defaultService = d.Service
	}

	// base_domain may use {pier.tld} so the same manifest works across
	// contributors who installed pier on different TLDs (e.g.
	// `base_domain = "myapp.{pier.tld}"`). Empty falls back to the
	// composed `<name>.<tld>` shape.
	baseDomain := m.Project.BaseDomain
	if baseDomain == "" {
		baseDomain = m.Project.Name + "." + cfg.TLD
	} else {
		baseDomain, err = adapter.ExpandPierTokens(baseDomain, cfg.TLD)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("project.base_domain: %w", err)
		}
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
			BaseDomain:     baseDomain,
			TLD:            cfg.TLD,
			WorktreePath:   info.Toplevel,
			Stack:          m.Stack,
			Expose:         m.Expose,
			DefaultService: defaultService,
			Env:            m.Env,
			TraefikNetwork: cfg.EffectiveTraefikNetwork(),
			Out:            out,
			Err:            errW,
		},
	}, nil
}
