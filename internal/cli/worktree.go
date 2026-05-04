package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/materialize"
	sluglib "github.com/LeoPartt/pier/internal/slug"
	"github.com/LeoPartt/pier/internal/worktree"
)

func newWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage git worktrees with pier-aware materialization",
	}
	cmd.AddCommand(newWorktreeAddCmd(), newWorktreeRmCmd(), newWorktreeCleanCmd())
	return cmd
}

type wtAddOpts struct {
	branch           string
	from             string
	up               bool
	ignoreHookErrors bool
}

func newWorktreeAddCmd() *cobra.Command {
	var opts wtAddOpts
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Create a worktree, materialize symlinks/snapshots, optionally pier up",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeAdd(cmd, args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.branch, "branch", "b", "", "create a new branch with this name (mirrors `git worktree add -b`)")
	f.StringVar(&opts.from, "from", "", "fork the new branch from this ref (default: manifest [worktree].base_ref, then main/master)")
	f.BoolVar(&opts.up, "up", false, "run `pier up` in the new worktree after materialization")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "do not roll back the worktree when a [materialize].post_create command fails")
	return cmd
}

func runWorktreeAdd(cmd *cobra.Command, target string, opts wtAddOpts) error {
	info, err := worktree.Detect()
	if err != nil {
		return err
	}
	abs, _, err := createWorktreeAt(info.PrimaryPath, target, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if opts.up {
		// CLI re-execs pier up to inherit user terminal; the API path
		// uses runUp directly. Both flow through the same compose adapter.
		return runPierIn(cmd, abs, "up")
	}
	return nil
}

// createWorktreeAt builds the new worktree under primary, materializes
// symlinks/snapshots, and reports the absolute path + branch name. Up is
// not handled here — callers that want it call runUp (API) or re-exec
// pier up (CLI). Shared by runWorktreeAdd and the REST POST handler so
// the two stay in lock-step on git args, materialize order, and
// snapshot pre-creation.
func createWorktreeAt(primary, target string, opts wtAddOpts, out, errOut io.Writer) (string, string, error) {
	m, err := manifest.Load(primary)
	if err != nil {
		return "", "", fmt.Errorf("primary manifest: %w (hint: run `pier init` in the primary worktree first)", err)
	}

	abs, err := resolveWorktreePath(primary, target, effectiveWorktreeDir(m))
	if err != nil {
		return "", "", err
	}

	branch := opts.branch
	if branch == "" {
		branch = filepath.Base(abs)
	}

	gitArgs := []string{"worktree", "add"}
	branchCreated := false
	if localBranchExists(primary, branch) {
		// Branch already exists: check it out at the new path. No -b, no
		// base ref — git refuses both for an existing branch.
		gitArgs = append(gitArgs, abs, branch)
		fmt.Fprintf(out, "  checking out existing branch %s\n", branch)
	} else {
		branchCreated = true
		gitArgs = append(gitArgs, "-b", branch, abs)
		if ref := pickBaseRef(opts.from, m.Worktree.BaseRef, primary); ref != "" {
			gitArgs = append(gitArgs, ref)
			fmt.Fprintf(out, "  creating branch %s from %s\n", branch, ref)
		} else {
			fmt.Fprintf(out, "  creating branch %s from HEAD\n", branch)
		}
	}
	git := exec.Command("git", gitArgs...)
	git.Dir = primary
	git.Stdout = out
	git.Stderr = errOut
	if err := git.Run(); err != nil {
		return "", "", fmt.Errorf("git worktree add: %w", err)
	}

	// Every step from here on is part of "build the worktree". A failure
	// in any of them leaves the user with a half-built worktree they
	// didn't ask for; roll back so re-running `pier worktree add` can
	// retry from a clean slate. --ignore-hook-errors only covers the
	// post_create hook — pre-hook failures (snapshot pre-create,
	// materialize symlinks/snapshots) are infra mistakes the user
	// should fix before retrying, not script-level glitches to skip.
	if err := preCreateSnapshotDirs(primary, abs, m.Materialize.Snapshots, out); err != nil {
		fmt.Fprintf(errOut, "! snapshot pre-create failed, rolling back: %v\n", err)
		rollbackWorktreeAdd(primary, abs, branch, branchCreated, errOut)
		return abs, branch, err
	}
	if err := materialize.Apply(primary, abs, m.Materialize, out); err != nil {
		fmt.Fprintf(errOut, "! materialize failed, rolling back: %v\n", err)
		rollbackWorktreeAdd(primary, abs, branch, branchCreated, errOut)
		return abs, branch, err
	}

	hc := buildHookContext(primary, abs, branch, m, errOut)
	if err := materialize.RunHooks("post_create", m.Materialize.PostCreate, hc, out, errOut); err != nil {
		if opts.ignoreHookErrors {
			fmt.Fprintf(errOut, "! post_create failed (continuing because --ignore-hook-errors): %v\n", err)
		} else {
			fmt.Fprintf(errOut, "! post_create failed, rolling back: %v\n", err)
			rollbackWorktreeAdd(primary, abs, branch, branchCreated, errOut)
			return abs, branch, fmt.Errorf("post_create hook: %w", err)
		}
	}

	fmt.Fprintf(out, "✓ worktree ready: %s\n", abs)
	return abs, branch, nil
}

// rollbackWorktreeAdd undoes a partially completed `pier worktree add`
// after a post_create failure: force-remove the worktree, then delete
// the branch only when WE created it in this same call. An existing
// branch (checked out into a fresh worktree) is left alone — the user
// had it before us, so we don't get to delete it.
func rollbackWorktreeAdd(primary, abs, branch string, branchCreated bool, errOut io.Writer) {
	rm := exec.Command("git", "worktree", "remove", "--force", abs)
	rm.Dir = primary
	rm.Stdout = errOut
	rm.Stderr = errOut
	if err := rm.Run(); err != nil {
		// Prune so git's worktree list stays consistent even if rm
		// failed mid-way (typical: root-owned files in a snapshot dir).
		prune := exec.Command("git", "worktree", "prune")
		prune.Dir = primary
		_ = prune.Run()
		fmt.Fprintf(errOut, "  ! rollback: git worktree remove failed: %v (dir may need `sudo rm -rf %s`)\n", err, abs)
	}
	if branchCreated {
		del := exec.Command("git", "branch", "-D", branch)
		del.Dir = primary
		del.Stdout = errOut
		del.Stderr = errOut
		if err := del.Run(); err != nil {
			fmt.Fprintf(errOut, "  ! rollback: git branch -D %s failed: %v\n", branch, err)
		}
	}
}

// buildHookContext assembles the PIER_* env exposed to materialize hooks.
// The base_domain is best-effort: if infra config can't be loaded
// (uninstalled pier, fresh checkout), we leave it empty rather than
// fail — hooks that don't need it shouldn't break, and ones that do
// can detect the empty value. Failures still surface to errOut so a
// silent empty PIER_BASE_DOMAIN/PIER_SLUG isn't mistaken for a bug
// in the script.
func buildHookContext(primary, current, branch string, m *manifest.Manifest, errOut io.Writer) materialize.HookContext {
	hc := materialize.HookContext{
		WorktreePath: current,
		PrimaryPath:  primary,
		Branch:       branch,
		ProjectName:  m.Project.Name,
	}
	if s, err := sluglib.FromBranch(branch); err == nil {
		hc.Slug = s
	} else if errOut != nil {
		fmt.Fprintf(errOut, "! hook context: slug derivation failed for branch %q: %v (PIER_SLUG empty)\n", branch, err)
	}
	paths, err := infra.DefaultPaths()
	if err != nil {
		if errOut != nil {
			fmt.Fprintf(errOut, "! hook context: infra paths unavailable: %v (PIER_BASE_DOMAIN empty)\n", err)
		}
		return hc
	}
	cfg, err := infra.LoadConfig(paths)
	if err != nil {
		if errOut != nil {
			fmt.Fprintf(errOut, "! hook context: infra config unavailable: %v (PIER_BASE_DOMAIN empty)\n", err)
		}
		return hc
	}
	if m.Project.BaseDomain == "" {
		hc.BaseDomain = m.Project.Name + "." + cfg.TLD
	} else if expanded, err := adapter.ExpandPierTokens(m.Project.BaseDomain, cfg.TLD); err == nil {
		hc.BaseDomain = expanded
	} else if errOut != nil {
		fmt.Fprintf(errOut, "! hook context: base_domain expansion failed: %v (PIER_BASE_DOMAIN empty)\n", err)
	}
	return hc
}

// preCreateSnapshotDirs makes sure every snapshot path exists in the new
// worktree as our user, even when the primary doesn't have it. Without
// this, the next `pier up` would let the docker daemon bind-mount-create
// the path as root, locking the workload out of its own data dir.
func preCreateSnapshotDirs(primary, current string, snapshots []string, out io.Writer) error {
	for _, entry := range snapshots {
		src := filepath.Join(primary, entry)
		dst := filepath.Join(current, entry)
		// Skip when materialize.Apply will populate dst from src.
		if _, err := os.Stat(src); err == nil {
			continue
		}
		// Skip when something is already there in the new worktree.
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fmt.Errorf("pre-create %s: %w", dst, err)
		}
		fmt.Fprintf(out, "✓ pre-created empty dir %s (no source on primary)\n", dst)
	}
	return nil
}

type wtRmOpts struct {
	skipDown         bool
	force            bool
	purge            bool
	ignoreHookErrors bool
}

func newWorktreeRmCmd() *cobra.Command {
	var opts wtRmOpts
	cmd := &cobra.Command{
		Use:   "rm <path>",
		Short: "Stop the workload, run git worktree remove, optionally purge snapshots",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeRm(cmd, args[0], opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.skipDown, "skip-down", false, "do not run pier down (use when the workload is already stopped)")
	f.BoolVar(&opts.force, "force", false, "pass --force to git worktree remove")
	f.BoolVar(&opts.purge, "purge", false, "run pier down --purge to wipe snapshot copies before removal")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "continue removal when a [materialize].pre_remove command fails")
	return cmd
}

func runWorktreeRm(cmd *cobra.Command, target string, opts wtRmOpts) error {
	info, err := worktree.Detect()
	if err != nil {
		return err
	}
	primary := info.PrimaryPath

	abs, err := resolveExistingWorktreePath(primary, target)
	if err != nil {
		return err
	}

	// pre_remove runs while the workload is still up: the canonical use
	// case is dumping a DB into a backup file before pier down stops the
	// container. Failure aborts the whole rm path (no down, no git rm)
	// unless --ignore-hook-errors is set.
	if err := runPreRemoveHook(primary, abs, opts.ignoreHookErrors, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		return err
	}

	if !opts.skipDown {
		args := []string{"down"}
		if opts.purge {
			args = append(args, "--purge")
		}
		// Best-effort: pier down errors when nothing is up. Don't bail.
		_ = runPierIn(cmd, abs, args...)
	}

	return removeWorktreeAt(primary, abs, opts.force, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// runPreRemoveHook executes [materialize].pre_remove against the
// worktree at abs while it is still up. Returns the hook error (so the
// caller can abort) unless ignore is true, in which case it logs and
// returns nil.
//
// A malformed manifest is fatal here for the same reason it is in
// createWorktreeAt: silently skipping pre_remove turns "I forgot a
// comma in .pier.toml" into "my DB backup never ran" with no warning.
func runPreRemoveHook(primary, abs string, ignore bool, out, errOut io.Writer) error {
	m, err := manifest.Load(primary)
	if err != nil {
		return fmt.Errorf("primary manifest: %w", err)
	}
	if len(m.Materialize.PreRemove) == 0 {
		return nil
	}
	branch := ""
	if info, err := worktree.DetectFrom(abs); err == nil {
		branch = info.Branch
	} else {
		fmt.Fprintf(errOut, "! pre_remove: could not detect branch at %s: %v (PIER_BRANCH/PIER_SLUG will be empty)\n", abs, err)
	}
	hc := buildHookContext(primary, abs, branch, m, errOut)
	if err := materialize.RunHooks("pre_remove", m.Materialize.PreRemove, hc, out, errOut); err != nil {
		if ignore {
			fmt.Fprintf(errOut, "! pre_remove failed (continuing because --ignore-hook-errors): %v\n", err)
			return nil
		}
		return fmt.Errorf("pre_remove hook: %w (use --ignore-hook-errors to remove anyway)", err)
	}
	return nil
}

// resolveExistingWorktreePath resolves <target> the same way
// `pier worktree add` would have placed it (effectiveWorktreeDir(m) —
// manifest, then prefs.toml, then the built-in default) and errors when
// the resulting path doesn't exist on disk. Pulled out so the CLI and
// API delete paths share resolution + existence semantics.
func resolveExistingWorktreePath(primary, target string) (string, error) {
	var m *manifest.Manifest
	if loaded, err := manifest.Load(primary); err == nil {
		m = loaded
	}
	abs, err := resolveWorktreePath(primary, target, effectiveWorktreeDir(m))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		return abs, fmt.Errorf("worktree %s does not exist", abs)
	}
	return abs, nil
}

// removeWorktreeAt runs `git worktree remove` against primary, with --force
// when force is true. Caller is responsible for stopping the workload
// first (CLI does runPierIn down; API does runDown via dailyForWorktree).
//
// On failure, runs `git worktree prune` so that even when the rm hit a
// permission error mid-way (typical when a container left root-owned
// files in a bind-mounted dir — see AGENTS.md pitfall #4), git's
// internal worktree list stays consistent. The dir itself may need a
// `sudo rm -rf` to fully clean up.
func removeWorktreeAt(primary, abs string, force bool, out, errOut io.Writer) error {
	gitArgs := []string{"worktree", "remove"}
	if force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, abs)

	git := exec.Command("git", gitArgs...)
	git.Dir = primary
	git.Stdout = out
	git.Stderr = errOut
	if err := git.Run(); err != nil {
		prune := exec.Command("git", "worktree", "prune")
		prune.Dir = primary
		_ = prune.Run()
		return fmt.Errorf("git worktree remove: %w", err)
	}
	fmt.Fprintf(out, "✓ removed worktree %s\n", abs)
	return nil
}

func newWorktreeCleanCmd() *cobra.Command {
	var opts wtRmOpts
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Stop and remove every secondary worktree of the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeClean(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.skipDown, "skip-down", false, "do not run pier down on each worktree first")
	f.BoolVar(&opts.force, "force", false, "pass --force to git worktree remove")
	f.BoolVar(&opts.purge, "purge", false, "run pier down --purge to wipe snapshot copies")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "continue removing each worktree when a [materialize].pre_remove command fails")
	return cmd
}

func runWorktreeClean(cmd *cobra.Command, opts wtRmOpts) error {
	info, err := worktree.Detect()
	if err != nil {
		return err
	}

	listCmd := exec.Command("git", "worktree", "list", "--porcelain")
	listCmd.Dir = info.PrimaryPath
	listOut, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("git worktree list: %w", err)
	}

	var paths []string
	for _, line := range strings.Split(string(listOut), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		p := strings.TrimPrefix(line, "worktree ")
		if p == info.PrimaryPath {
			continue
		}
		paths = append(paths, p)
	}

	if len(paths) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no secondary worktrees to clean")
		return nil
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cleaning %d worktree(s):\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(out, "→ %s\n", p)
		if err := runWorktreeRm(cmd, p, opts); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ! %v\n", err)
		}
	}
	return nil
}

// localBranchExists reports whether <name> is a local branch in the repo
// at primary.
func localBranchExists(primary, name string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = primary
	return cmd.Run() == nil
}

// pickBaseRef resolves the ref to fork the new worktree's branch from. The
// flag wins, then the manifest setting, then a probe for the conventional
// default branches in the primary repo. Returns "" to let git pick HEAD —
// the current behaviour was that, kept as the last-resort fallback so this
// change can't break a repo that uses neither main nor master.
func pickBaseRef(flag, manifest, primary string) string {
	if flag != "" {
		return flag
	}
	if manifest != "" {
		return manifest
	}
	for _, candidate := range []string{"main", "master"} {
		c := exec.Command("git", "rev-parse", "--verify", "--quiet", candidate)
		c.Dir = primary
		if c.Run() == nil {
			return candidate
		}
	}
	return ""
}

// resolveWorktreePath turns a `pier worktree add <name>` argument into an
// absolute path. When <name> contains no path separator and a worktree
// dir is configured (manifest, prefs, or built-in default), we place
// the new worktree there — letting users use a short name (`pier
// worktree add feat-x`) and keep all branches under one folder.
// Anything else is treated as an explicit path.
//
// configuredDir is interpreted as:
//
//   - "~" or "~/..." → expanded against $HOME, then joined with target
//   - absolute path → joined directly with target
//   - relative path → relative to the primary worktree (project root)
//
// This lets a project pin "./worktrees" without leaking out, while a
// user pref like "~/wt/myproj" or "/srv/worktrees" lands at the
// absolute location they asked for.
func resolveWorktreePath(primary, target, configuredDir string) (string, error) {
	hasSep := strings.ContainsRune(target, filepath.Separator)
	if hasSep || configuredDir == "" {
		return filepath.Abs(target)
	}
	base, err := expandWorktreeDir(primary, configuredDir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(base, target))
}

// expandWorktreeDir resolves a configured worktree dir into an
// absolute path. See resolveWorktreePath for the supported forms.
func expandWorktreeDir(primary, dir string) (string, error) {
	if strings.HasPrefix(dir, "~") && (len(dir) == 1 || dir[1] == '/') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s: %w", dir, err)
		}
		return filepath.Join(home, dir[1:]), nil
	}
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	return filepath.Join(primary, dir), nil
}

// runPierIn invokes the currently running pier binary in dir with subargs.
// We re-exec rather than dispatch via cobra so the spawned command runs
// against the right working directory (worktree.Detect uses cwd).
func runPierIn(cmd *cobra.Command, dir string, subargs ...string) error {
	bin, err := os.Executable()
	if err != nil {
		bin = "pier"
	}
	c := exec.Command(bin, subargs...)
	c.Dir = dir
	c.Stdin = cmd.InOrStdin()
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
}
