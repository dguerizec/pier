package cli

import "testing"

func TestManagedBy(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/opt/homebrew/bin/pier", "homebrew"},
		{"/usr/local/Cellar/pier/0.1.0/bin/pier", "homebrew"},
		{"/home/linuxbrew/.linuxbrew/bin/pier", "homebrew"},
		{"/usr/bin/pier", "the system package manager"},
		{"/usr/sbin/pier", "the system package manager"},
		{"/bin/pier", "the system package manager"},
		{"/sbin/pier", "the system package manager"},
		{"/home/alice/.local/bin/pier", ""},
		{"/usr/local/bin/pier", ""},
		{"/tmp/pier", ""},
		{"/home/alice/go/bin/pier", ""},
	}
	for _, c := range cases {
		if got := managedBy(c.path); got != c.want {
			t.Errorf("managedBy(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
