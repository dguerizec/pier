package slug

import (
	"errors"
	"testing"
)

func TestFromBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   string
	}{
		// DESIGN §5.1 reference table (with the main/master → branch name
		// rule, not the historical "dev" special case)
		{"main", "main"},
		{"master", "master"},
		{"feat/foo-bar", "foo-bar"},
		{"fix/CROPS-42", "crops-42"},
		{"chore/update-deps", "update-deps"},
		{"worktree-quickfix", "quickfix"},
		{"release/v1.2", "release-v1-2"},

		// Other conventional prefixes
		{"docs/api", "api"},
		{"perf/hot-loop", "hot-loop"},
		{"refactor/db_layer", "db-layer"},
		{"style/format", "format"},
		{"test/integration", "integration"},
		{"ci/github", "github"},
		{"build/cross", "cross"},
		{"revert/abc", "abc"},

		// Edge cases that should still produce a valid slug
		{"HOTFIX", "hotfix"},
		{"feat/Foo_Bar.baz", "foo-bar-baz"},
		{"feat//double-slash", "double-slash"},
		{"123-numeric-start", "123-numeric-start"},
		{"  feat/spaced  ", "spaced"},
	}
	for _, c := range cases {
		t.Run(c.branch, func(t *testing.T) {
			got, err := FromBranch(c.branch)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("FromBranch(%q) = %q, want %q", c.branch, got, c.want)
			}
		})
	}
}

func TestFromBranch_Errors(t *testing.T) {
	cases := []struct {
		branch string
		want   error
	}{
		{"", ErrEmpty},
		{"feat/", ErrEmpty},
		{"////", ErrEmpty},
		{"---", ErrEmpty},
		{"worktree-", ErrEmpty},
	}
	for _, c := range cases {
		t.Run(c.branch, func(t *testing.T) {
			_, err := FromBranch(c.branch)
			if !errors.Is(err, c.want) {
				t.Errorf("FromBranch(%q) err = %v, want %v", c.branch, err, c.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	good := []string{"dev", "foo-bar", "x", "release-v1-2", "abc123", "1ab"}
	bad := []string{"", "-foo", "foo-", "Foo", "foo_bar", "foo.bar", "foo bar"}

	for _, s := range good {
		if err := Validate(s); err != nil {
			t.Errorf("Validate(%q) unexpected error: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := Validate(s); err == nil {
			t.Errorf("Validate(%q) expected error, got nil", s)
		}
	}
}
