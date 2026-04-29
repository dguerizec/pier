package materialize

import (
	"io"
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

func TestApply_ReplacesEmptyDir(t *testing.T) {
	primary, current := setup(t)
	mustMkdir(t, filepath.Join(primary, "secrets"))
	mustWrite(t, filepath.Join(primary, "secrets", "key"), "k")
	// docker bind-mount aftermath: an empty dir already exists in current
	mustMkdir(t, filepath.Join(current, "secrets"))

	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{"secrets"}}, nil); err != nil {
		t.Fatal(err)
	}
	mustBeSymlink(t, filepath.Join(current, "secrets"), filepath.Join(primary, "secrets"))
}

func TestApply_PreservesNonEmptyDir(t *testing.T) {
	primary, current := setup(t)
	mustMkdir(t, filepath.Join(primary, "secrets"))
	mustWrite(t, filepath.Join(primary, "secrets", "key"), "from-primary")
	mustMkdir(t, filepath.Join(current, "secrets"))
	mustWrite(t, filepath.Join(current, "secrets", "local"), "worktree-only")

	if err := Apply(primary, current, manifest.Materialize{Symlinks: []string{"secrets"}}, nil); err != nil {
		t.Fatal(err)
	}
	// Should remain a real directory, not a symlink.
	info, err := os.Lstat(filepath.Join(current, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("non-empty dir was replaced by a symlink; user data could have been lost")
	}
}

func TestApply_NoOpOnPrimary(t *testing.T) {
	primary, _ := setup(t)
	mustWrite(t, filepath.Join(primary, ".env"), "x")
	if err := Apply(primary, primary, manifest.Materialize{Symlinks: []string{".env"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApply_Snapshot_CopiesTree(t *testing.T) {
	primary, current := setup(t)
	mustMkdir(t, filepath.Join(primary, "data-dev"))
	mustWrite(t, filepath.Join(primary, "data-dev", "app.db"), "primary-db")
	mustWrite(t, filepath.Join(primary, "data-dev", "fixtures", "user.json"), `{"id":1}`)

	if err := Apply(primary, current, manifest.Materialize{Snapshots: []string{"data-dev"}}, nil); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(current, "data-dev", "app.db"))
	if err != nil || string(body) != "primary-db" {
		t.Errorf("data-dev/app.db = %q, %v", body, err)
	}
	body, _ = os.ReadFile(filepath.Join(current, "data-dev", "fixtures", "user.json"))
	if string(body) != `{"id":1}` {
		t.Errorf("nested file not copied: %q", body)
	}

	// Mutating the copy must not leak back to primary.
	if err := os.WriteFile(filepath.Join(current, "data-dev", "app.db"), []byte("worktree-only"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(primary, "data-dev", "app.db"))
	if string(body) != "primary-db" {
		t.Errorf("primary mutated through snapshot: %q", body)
	}
}

func TestApply_Snapshot_ReplacesEmptyDir(t *testing.T) {
	primary, current := setup(t)
	mustMkdir(t, filepath.Join(primary, "data-dev"))
	mustWrite(t, filepath.Join(primary, "data-dev", "app.db"), "x")
	mustMkdir(t, filepath.Join(current, "data-dev"))

	if err := Apply(primary, current, manifest.Materialize{Snapshots: []string{"data-dev"}}, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(current, "data-dev", "app.db"))
	if string(body) != "x" {
		t.Errorf("snapshot did not populate empty dir: %q", body)
	}
}

func TestApply_Snapshot_PreservesExisting(t *testing.T) {
	primary, current := setup(t)
	mustMkdir(t, filepath.Join(primary, "data-dev"))
	mustWrite(t, filepath.Join(primary, "data-dev", "app.db"), "primary")
	mustMkdir(t, filepath.Join(current, "data-dev"))
	mustWrite(t, filepath.Join(current, "data-dev", "app.db"), "worktree-existing")

	if err := Apply(primary, current, manifest.Materialize{Snapshots: []string{"data-dev"}}, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(current, "data-dev", "app.db"))
	if string(body) != "worktree-existing" {
		t.Errorf("snapshot overwrote a populated worktree dir: %q", body)
	}
}

func TestApply_Snapshot_MergesNewFilesIntoNonEmptyDir(t *testing.T) {
	primary, current := setup(t)
	// primary has the canonical seed; current already holds a worktree-local
	// file, plus an entry that overlaps with primary by name.
	mustMkdir(t, filepath.Join(primary, "data-dev"))
	mustWrite(t, filepath.Join(primary, "data-dev", "shared.db"), "from-primary")
	mustWrite(t, filepath.Join(primary, "data-dev", "fixtures", "user.json"), `{"id":1}`)
	mustWrite(t, filepath.Join(primary, "data-dev", "fixtures", "new.json"), `{"new":true}`)

	mustMkdir(t, filepath.Join(current, "data-dev"))
	mustWrite(t, filepath.Join(current, "data-dev", "shared.db"), "worktree-mutated")
	mustWrite(t, filepath.Join(current, "data-dev", "fixtures", "user.json"), `{"id":99}`)
	mustWrite(t, filepath.Join(current, "data-dev", "local-only.txt"), "kept")

	if err := Apply(primary, current, manifest.Materialize{Snapshots: []string{"data-dev"}}, nil); err != nil {
		t.Fatal(err)
	}

	// Existing entries at dst are preserved verbatim — file-level skip.
	body, _ := os.ReadFile(filepath.Join(current, "data-dev", "shared.db"))
	if string(body) != "worktree-mutated" {
		t.Errorf("shared.db overwritten: %q", body)
	}
	body, _ = os.ReadFile(filepath.Join(current, "data-dev", "fixtures", "user.json"))
	if string(body) != `{"id":99}` {
		t.Errorf("nested file overwritten: %q", body)
	}
	body, _ = os.ReadFile(filepath.Join(current, "data-dev", "local-only.txt"))
	if string(body) != "kept" {
		t.Errorf("local-only.txt lost: %q", body)
	}
	// Files only in primary now land at dst.
	body, err := os.ReadFile(filepath.Join(current, "data-dev", "fixtures", "new.json"))
	if err != nil || string(body) != `{"new":true}` {
		t.Errorf("new.json not merged: body=%q err=%v", body, err)
	}
}

func TestApply_Snapshot_FilePreservedNotOverwritten(t *testing.T) {
	primary, current := setup(t)
	mustWrite(t, filepath.Join(primary, "config.toml"), "from-primary")
	mustWrite(t, filepath.Join(current, "config.toml"), "worktree-local")

	if err := Apply(primary, current, manifest.Materialize{Snapshots: []string{"config.toml"}}, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(current, "config.toml"))
	if string(body) != "worktree-local" {
		t.Errorf("file snapshot overwrote existing dst: %q", body)
	}
}

func TestPurge(t *testing.T) {
	_, current := setup(t)
	mustMkdir(t, filepath.Join(current, "data-dev"))
	mustWrite(t, filepath.Join(current, "data-dev", "app.db"), "x")
	mustWrite(t, filepath.Join(current, ".env"), "kept")

	err := Purge(current, manifest.Materialize{
		Snapshots: []string{"data-dev"},
		Symlinks:  []string{".env"}, // must NOT be touched
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(current, "data-dev")); !os.IsNotExist(err) {
		t.Error("snapshot not purged")
	}
	if _, err := os.Stat(filepath.Join(current, ".env")); err != nil {
		t.Errorf(".env should be untouched, got %v", err)
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
