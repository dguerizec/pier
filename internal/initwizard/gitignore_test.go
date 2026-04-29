package initwizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeDirGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{".pier/worktrees", ""},     // already covered by .pier/
		{".pier", ""},               // ditto
		{"worktrees", "worktrees/"}, // explicit relative path
		{"./trees", "trees/"},
		{"/abs/elsewhere", ""}, // absolute outside repo → skip
	}
	for _, c := range cases {
		got := WorktreeDirGitignoreEntry(dir, c.in)
		if got != c.want {
			t.Errorf("WorktreeDirGitignoreEntry(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	if err := EnsureGitignore(dir, ".pier/"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), ".pier/") {
		t.Errorf("missing entry: %q", body)
	}

	// idempotent
	if err := EnsureGitignore(dir, ".pier/"); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(path)
	if strings.Count(string(body2), ".pier/") != 1 {
		t.Errorf("entry duplicated: %q", body2)
	}

	// appends a newline before adding when file lacks trailing newline
	if err := os.WriteFile(path, []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir, "bar"); err != nil {
		t.Fatal(err)
	}
	body3, _ := os.ReadFile(path)
	if string(body3) != "foo\nbar\n" {
		t.Errorf("unexpected body %q", body3)
	}
}
