package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/manifest"
	"github.com/dguerizec/pier/internal/materialize"
	"github.com/dguerizec/pier/internal/runtimevalues"
	sluglib "github.com/dguerizec/pier/internal/slug"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/worktree"
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

// loadManifestForWorktree loads <info.Toplevel>/.pier.toml, with a fallback
// to the primary worktree when the secondary doesn't have one. Rationale:
// the manifest is project config, not source code. Users typically do not
// commit `.pier.toml` (the CLI init wizard gitignores it by default), so a
// freshly-created worktree won't carry the manifest in its initial
// checkout. Falling back to the primary lets `pier up` and friends work
// from any worktree without forcing the user to commit-and-push first.
//
// When the secondary HAS its own `.pier.toml`, that one wins — which is
// what you want if a branch deliberately overrides the manifest.
func loadManifestForWorktree(info *worktree.Info) (*manifest.Manifest, error) {
	root, err := manifestRootForWorktree(info)
	if err != nil {
		return nil, err
	}
	return manifest.Load(root)
}

func manifestRootForWorktree(info *worktree.Info) (string, error) {
	if _, err := os.Stat(filepath.Join(info.Toplevel, manifest.FileName)); err == nil {
		return info.Toplevel, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info.PrimaryPath != "" && info.PrimaryPath != info.Toplevel {
		if _, err := os.Stat(filepath.Join(info.PrimaryPath, manifest.FileName)); err == nil {
			return info.PrimaryPath, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return info.Toplevel, manifest.ErrNotFound
}

// loadManifestForWorkloadPath is the lookup variant used when we only have
// a worktree path (state row) and need the manifest. Detects the worktree
// to find its primary, then defers to loadManifestForWorktree.
func loadManifestForWorkloadPath(worktreePath string) (*manifest.Manifest, error) {
	info, err := worktree.DetectFrom(worktreePath)
	if err != nil {
		// Fall back to the original behaviour — surfaces the
		// manifest-not-found error if the path is broken.
		return manifest.Load(worktreePath)
	}
	return loadManifestForWorktree(info)
}

// resolveDaily detects the worktree, loads the manifest, computes the slug
// (PIER_SLUG env or --slug flag override the branch derivation), and opens
// the state DB. When --slug points at a different worktree than cwd, the
// returned context targets that worktree's filesystem too — bind mounts
// and materialization need it. Caller MUST defer d.State.Close() on success.
//
// The *cobra.Command supplies the writers that end up in d.Ctx.Out and
// d.Ctx.Err — i.e. where the compose adapter streams `docker compose
// up/down/logs` output. Tests can SetOut/SetErr a buffer on the command
// and capture everything the worktree-scoped flow prints, including
// subprocess output.
func resolveDaily(cmd *cobra.Command, slugOverride string) (*daily, error) {
	return resolveDailyMode(cmd, slugOverride, false)
}

func resolveDailyFresh(cmd *cobra.Command, slugOverride string) (*daily, error) {
	return resolveDailyMode(cmd, slugOverride, true)
}

func resolveDailyMode(cmd *cobra.Command, slugOverride string, refreshValues bool) (*daily, error) {
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
	return dailyForWorktreeMode(info, slug, cmd.OutOrStdout(), cmd.ErrOrStderr(), refreshValues)
}

// dailyForWorktree builds a daily for a pre-resolved worktree info. Used
// by resolveDaily (via cwd) and the REST API (via state-DB path lookup).
// When slug is empty, derive it from the worktree's branch.
func dailyForWorktree(info *worktree.Info, slug string, out, errW io.Writer) (*daily, error) {
	return dailyForWorktreeMode(info, slug, out, errW, false)
}

// dailyForWorktreeFresh is the up-path variant: it always re-runs
// hooks.resolve_values before parsing the final manifest.
func dailyForWorktreeFresh(info *worktree.Info, slug string, out, errW io.Writer) (*daily, error) {
	return dailyForWorktreeMode(info, slug, out, errW, true)
}

func dailyForWorktreeMode(info *worktree.Info, slug string, out, errW io.Writer, refreshValues bool) (*daily, error) {
	if slug == "" {
		derived, err := sluglib.FromBranch(info.Branch)
		if err != nil {
			return nil, fmt.Errorf("derive slug from branch %q: %w", info.Branch, err)
		}
		slug = derived
	}

	manifestRoot, err := manifestRootForWorktree(info)
	if err != nil {
		return nil, err
	}
	bootstrap, err := manifest.LoadBootstrap(manifestRoot)
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

	m, resolvedValueEnv, err := loadDailyManifest(
		manifestRoot,
		info,
		slug,
		bootstrap,
		cfg,
		refreshValues,
		out,
		errW,
	)
	if err != nil {
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

	ctx := adapter.Ctx{
		Project:        m.Project.Name,
		Slug:           slug,
		BaseDomain:     baseDomain,
		TLD:            cfg.TLD,
		WorktreePath:   info.Toplevel,
		Stack:          m.Stack,
		Expose:         m.Expose,
		Service:        m.Service,
		DefaultService: defaultService,
		Env:            m.Env,
		TraefikNetwork: cfg.EffectiveTraefikNetwork(),
		Out:            out,
		Err:            errW,
	}
	stackEnv, err := adapter.ExpandEnvBlock(m.Stack.Env, ctx)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("stack.env: %w", err)
	}
	ctx.ComposeEnv = mergeComposeEnv(stackEnv, resolvedValueEnv)

	return &daily{
		Worktree: info,
		Manifest: m,
		Slug:     slug,
		Paths:    paths,
		Config:   cfg,
		State:    store,
		Ctx:      ctx,
	}, nil
}

// mergeComposeEnv combines user-named adapter variables with the reserved
// PIER_VALUE_* variables derived from resolve_values. Manifest validation
// reserves the entire PIER_ namespace, but resolved values deliberately win
// if an in-memory caller bypasses validation.
func mergeComposeEnv(stackEnv, resolvedValueEnv map[string]string) map[string]string {
	if len(stackEnv) == 0 && len(resolvedValueEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(stackEnv)+len(resolvedValueEnv))
	for name, value := range stackEnv {
		out[name] = value
	}
	for name, value := range resolvedValueEnv {
		out[name] = value
	}
	return out
}

func loadDailyManifest(
	manifestRoot string,
	info *worktree.Info,
	slug string,
	bootstrap *manifest.Bootstrap,
	cfg *infra.Config,
	refresh bool,
	out, errW io.Writer,
) (*manifest.Manifest, map[string]string, error) {
	command := bootstrap.Hooks.ResolveValues
	if command == "" {
		m, err := manifest.Load(manifestRoot)
		return m, nil, err
	}

	valuesPath := runtimevalues.Path(info.Toplevel)
	values, err := runtimevalues.Load(valuesPath)
	runResolver := refresh || errors.Is(err, os.ErrNotExist)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !refresh {
		return nil, nil, err
	}
	if err != nil {
		values = nil
	}

	if runResolver {
		baseDomain, err := resolvedBaseDomain(bootstrap.Project, cfg.TLD)
		if err != nil {
			return nil, nil, err
		}
		hc := materialize.HookContext{
			WorktreePath: info.Toplevel,
			PrimaryPath:  info.PrimaryPath,
			Slug:         slug,
			Branch:       info.Branch,
			BaseDomain:   baseDomain,
			ProjectName:  bootstrap.Project.Name,
			ValuesFile:   valuesPath,
		}
		env := hc.Env()
		if previousEnv, envErr := runtimevalues.Environment(values); envErr == nil {
			env = runtimevalues.MergeEnv(env, previousEnv)
		}
		if out != nil {
			fmt.Fprintf(out, "▸ resolve_values: %s\n", command)
		}
		values, err = runtimevalues.Resolve(command, info.Toplevel, env, errW)
		if err != nil {
			return nil, nil, err
		}
	}

	m, err := manifest.LoadResolved(manifestRoot, values)
	if err != nil {
		return nil, nil, err
	}
	composeEnv, err := runtimevalues.Environment(values)
	if err != nil {
		return nil, nil, err
	}
	if runResolver {
		if err := runtimevalues.Save(valuesPath, values); err != nil {
			return nil, nil, err
		}
	}
	return m, composeEnv, nil
}

func resolvedBaseDomain(project manifest.Project, tld string) (string, error) {
	if project.BaseDomain == "" {
		return project.Name + "." + tld, nil
	}
	expanded, err := adapter.ExpandPierTokens(project.BaseDomain, tld)
	if err != nil {
		return "", fmt.Errorf("project.base_domain: %w", err)
	}
	return expanded, nil
}
