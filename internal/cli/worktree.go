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

	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/materialize"
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
	branch string
	from   string
	up     bool
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
	if localBranchExists(primary, branch) {
		// Branch already exists: check it out at the new path. No -b, no
		// base ref — git refuses both for an existing branch.
		gitArgs = append(gitArgs, abs, branch)
		fmt.Fprintf(out, "  checking out existing branch %s\n", branch)
	} else {
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

	if err := preCreateSnapshotDirs(primary, abs, m.Materialize.Snapshots, out); err != nil {
		return abs, branch, err
	}
	if err := materialize.Apply(primary, abs, m.Materialize, out); err != nil {
		return abs, branch, err
	}
	fmt.Fprintf(out, "✓ worktree ready: %s\n", abs)
	return abs, branch, nil
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
	skipDown bool
	force    bool
	purge    bool
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
