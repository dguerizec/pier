package worktree

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetect_Primary(t *testing.T) {
	primary := setupRepo(t)

	info, err := DetectFrom(primary)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !info.IsPrimary {
		t.Errorf("IsPrimary = false, want true (gitdir=%q, common=%q)", info.GitDir, info.CommonDir)
	}
	if info.Branch != "main" {
		t.Errorf("Branch = %q, want main", info.Branch)
	}
	resolved, _ := filepath.EvalSymlinks(primary)
	if info.Toplevel != resolved {
		t.Errorf("Toplevel = %q, want %q", info.Toplevel, resolved)
	}
	if info.PrimaryPath != info.Toplevel {
		t.Errorf("PrimaryPath = %q, want %q", info.PrimaryPath, info.Toplevel)
	}
}

func TestDetect_Secondary(t *testing.T) {
	primary := setupRepo(t)
	secondary := filepath.Join(t.TempDir(), "wt")
	runGit(t, primary, "worktree", "add", "-b", "feat/foo", secondary)

	info, err := DetectFrom(secondary)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.IsPrimary {
		t.Errorf("IsPrimary = true, want false")
	}
	if info.Branch != "feat/foo" {
		t.Errorf("Branch = %q, want feat/foo", info.Branch)
	}
	resolvedPrimary, _ := filepath.EvalSymlinks(primary)
	if info.PrimaryPath != resolvedPrimary {
		t.Errorf("PrimaryPath = %q, want %q", info.PrimaryPath, resolvedPrimary)
	}
	if info.GitDir == info.CommonDir {
		t.Errorf("GitDir == CommonDir on secondary worktree (gitdir=%q)", info.GitDir)
	}
}

func TestDetect_NotARepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := DetectFrom(dir); err == nil {
		t.Fatal("expected error for non-repo, got nil")
	}
}

func TestDetect_DetachedHEAD(t *testing.T) {
	primary := setupRepo(t)
	sha := runGit(t, primary, "rev-parse", "HEAD")
	runGit(t, primary, "checkout", "--detach", sha)

	_, err := DetectFrom(primary)
	if !errors.Is(err, ErrDetached) {
		t.Fatalf("err = %v, want ErrDetached", err)
	}
}

// setupRepo creates a fresh git repo with one empty commit on main and
// returns its path. Uses -c overrides so user identity is local to the repo.
func setupRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main", "-q")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init", "-q")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.email=test@pier.local",
		"-c", "user.name=pier-test",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(bytesTrimRight(out, '\n'))
}

func bytesTrimRight(b []byte, c byte) []byte {
	for len(b) > 0 && b[len(b)-1] == c {
		b = b[:len(b)-1]
	}
	return b
}
