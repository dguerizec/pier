package materialize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeoPartt/pier/internal/manifest"
)

func TestApply_CreatesSymlinks(t *testing.T) {
	primary, current := setup(t)
	mustWrite(t, filepath.Join(primary, ".env"), "FOO=bar")
	mustMkdir(t, filepath.Join(primary, "secrets"))
	mustWrite(t, filepath.Join(primary, "secrets", "api.key"), "sekrit")

	err := Apply(primary, current, manifest.Materialize{
		Symlinks: []string{".env", "secrets"},
	}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	mustBeSymlink(t, filepath.Join(current, ".env"), filepath.Join(primary, ".env"))
	mustBeSymlink(t, filepath.Join(current, "secrets"), filepath.Join(primary, "secrets"))
}

func TestApply_Idempotent(t *testing.T) {
	primary, current := setup(t)
	mustWrite(t, filepath.Join(primary, ".env"), "x")

	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatal(err)
	}
	// second call should be a no-op
	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

func TestApply_PreservesExistingFile(t *testing.T) {
	primary, current := setup(t)
	mustWrite(t, filepath.Join(primary, ".env"), "FROM_PRIMARY")
	mustWrite(t, filepath.Join(current, ".env"), "WORKTREE_OWN")

	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(current, ".env"))
	if string(body) != "WORKTREE_OWN" {
		t.Errorf(".env was overwritten; got %q, want WORKTREE_OWN", body)
	}
}

func TestApply_SkipsMissingSource(t *testing.T) {
	primary, current := setup(t)
	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatalf("Apply with missing source should not error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(current, ".env")); !os.IsNotExist(err) {
		t.Error("dangling symlink created when source was missing")
	}
}

func TestApply_NoOpOnPrimary(t *testing.T) {
	primary, _ := setup(t)
	mustWrite(t, filepath.Join(primary, ".env"), "x")
	if err := Apply(primary, primary, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatal(err)
	}
}

// helpers

func setup(t *testing.T) (string, string) {
	t.Helper()
	primary := t.TempDir()
	current := t.TempDir()
	return primary, current
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustBeSymlink(t *testing.T, link, target string) {
	t.Helper()
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink %s: %v", link, err)
	}
	if dest != target {
		t.Errorf("symlink %s -> %s, want %s", link, dest, target)
	}
}
