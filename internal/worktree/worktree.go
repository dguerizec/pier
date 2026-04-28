// Package worktree resolves git worktree metadata via plumbing commands
// (DESIGN §5.2). All detection is shelling out to git; no libgit2 dep.
package worktree

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Info describes the git worktree pier is operating in.
type Info struct {
	Toplevel    string // absolute path of the current worktree
	GitDir      string // absolute path of $GIT_DIR (per-worktree under .git/worktrees/<name> for secondaries)
	CommonDir   string // absolute path of the primary worktree's .git
	Branch      string // current branch name (HEAD if detached)
	PrimaryPath string // absolute path of the primary worktree
	IsPrimary   bool   // true when the current worktree is the primary
}

// ErrDetached is returned when HEAD is detached. The caller decides whether
// that's recoverable (pier up cannot derive a slug from a detached HEAD).
var ErrDetached = errors.New("worktree: HEAD is detached")

// Detect inspects the git state from the current process working directory.
func Detect() (*Info, error) { return DetectFrom("") }

// DetectFrom inspects the git state from dir. Pass "" to use cwd.
func DetectFrom(dir string) (*Info, error) {
	toplevel, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	gitDir, err := git(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	commonDir, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	branch, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if branch == "HEAD" {
		return nil, ErrDetached
	}

	primary, err := primaryPath(dir)
	if err != nil {
		return nil, err
	}

	return &Info{
		Toplevel:    toplevel,
		GitDir:      gitDir,
		CommonDir:   commonDir,
		Branch:      branch,
		PrimaryPath: primary,
		IsPrimary:   gitDir == commonDir,
	}, nil
}

// Entry is one row of `git worktree list --porcelain`.
type Entry struct {
	Path   string // absolute worktree path
	Branch string // short branch name, "" when detached
}

// List returns every worktree registered against the repo containing dir.
// Pass "" to use the process working directory.
func List(dir string) ([]Entry, error) {
	out, err := gitRaw(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var cur Entry
	flush := func() {
		if cur.Path != "" {
			entries = append(entries, cur)
		}
		cur = Entry{}
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "":
			flush()
		}
	}
	flush()
	return entries, nil
}

func primaryPath(dir string) (string, error) {
	out, err := gitRaw(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree "), nil
		}
	}
	return "", errors.New("worktree: no entries in `git worktree list`")
}

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
