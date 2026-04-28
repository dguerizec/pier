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
	cmd.AddCommand(newWorktreeAddCmd(), newWorktreeRmCmd())
	return cmd
}

type wtAddOpts struct {
	branch string
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
	f.BoolVar(&opts.up, "up", false, "run `pier up` in the new worktree after materialization")
	return cmd
}

func runWorktreeAdd(cmd *cobra.Command, target string, opts wtAddOpts) error {
	info, err := worktree.Detect()
	if err != nil {
		return err
	}
	primary := info.PrimaryPath

	m, err := manifest.Load(primary)
	if err != nil {
		return fmt.Errorf("primary manifest: %w (hint: run `pier init` in the primary worktree first)", err)
	}

	abs, err := resolveWorktreePath(primary, target, m.Worktree.Dir)
	if err != nil {
		return err
	}

	gitArgs := []string{"worktree", "add"}
	if opts.branch != "" {
		gitArgs = append(gitArgs, "-b", opts.branch, abs)
	} else {
		gitArgs = append(gitArgs, abs)
	}
	git := exec.Command("git", gitArgs...)
	git.Dir = primary
	git.Stdout = cmd.OutOrStdout()
	git.Stderr = cmd.ErrOrStderr()
	if err := git.Run(); err != nil {
		return fmt.Errorf("git worktree add: %w", err)
	}

	if err := preCreateSnapshotDirs(primary, abs, m.Materialize.Snapshots, cmd.OutOrStdout()); err != nil {
		return err
	}
	if err := materialize.Apply(primary, abs, m.Materialize, cmd.OutOrStdout()); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ worktree ready: %s\n", abs)

	if opts.up {
		return runPierIn(cmd, abs, "up")
	}
	return nil
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
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("worktree %s does not exist", abs)
	}

	info, err := worktree.Detect()
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

	gitArgs := []string{"worktree", "remove"}
	if opts.force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, abs)

	git := exec.Command("git", gitArgs...)
	git.Dir = info.PrimaryPath
	git.Stdout = cmd.OutOrStdout()
	git.Stderr = cmd.ErrOrStderr()
	if err := git.Run(); err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ removed worktree %s\n", abs)
	return nil
}

// resolveWorktreePath turns a `pier worktree add <name>` argument into an
// absolute path. When <name> contains no path separator and the manifest
// declares a [worktree].dir, we place the new worktree there — letting
// users use a short name (`pier worktree add feat-x`) and keep all
// branches under one folder (`.claude/worktrees/feat-x`). Anything else
// is treated as an explicit path.
func resolveWorktreePath(primary, target, manifestDir string) (string, error) {
	hasSep := strings.ContainsRune(target, filepath.Separator)
	if !hasSep && manifestDir != "" {
		return filepath.Abs(filepath.Join(primary, manifestDir, target))
	}
	return filepath.Abs(target)
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
