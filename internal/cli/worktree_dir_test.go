package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorktreePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	primary := "/srv/proj"

	cases := []struct {
		name      string
		target    string
		configDir string
		want      string
	}{
		{"bare name + relative dir", "feat-x", ".pier/worktrees", "/srv/proj/.pier/worktrees/feat-x"},
		{"bare name + absolute dir", "feat-x", "/var/wt", "/var/wt/feat-x"},
		{"bare name + tilde dir", "feat-x", "~/wt/proj", filepath.Join(home, "wt/proj/feat-x")},
		{"bare name + plain tilde", "feat-x", "~", filepath.Join(home, "feat-x")},
		{"path target ignores configured dir", "/tmp/explicit", ".pier/worktrees", "/tmp/explicit"},
		// Explicit paths (containing a separator) resolve against cwd —
		// matches the pre-existing semantics. Asserted dynamically below.
		{"empty configured dir", "feat-x", "", "/srv/proj/feat-x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveWorktreePath(primary, c.target, c.configDir)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			// Empty configured dir → bare name resolves against cwd
			// (pre-existing semantics for explicit-path targets).
			if c.configDir == "" {
				want, _ := filepath.Abs(c.target)
				if got != want {
					t.Errorf("got %q, want %q", got, want)
				}
				return
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveWorktreePath_RelativeTargetIsCwdRelative(t *testing.T) {
	got, err := resolveWorktreePath("/srv/proj", "../sibling", ".pier/worktrees")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("../sibling")
	if got != want {
		t.Errorf("explicit relative path should resolve against cwd: got %q, want %q", got, want)
	}
}
