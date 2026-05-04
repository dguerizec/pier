package systemd

import (
	"strings"
	"testing"
)

func TestRender_UserUnit(t *testing.T) {
	body := Render(ScopeUser, "/usr/local/bin/pier")
	mustContain(t, body, "ExecStart=/usr/local/bin/pier serve")
	mustContain(t, body, "WantedBy=default.target")
	mustNotContain(t, body, "After=docker.service")
	mustContain(t, body, "After=default.target")
	mustContain(t, body, "Restart=on-failure")
}

func TestRender_SystemUnit(t *testing.T) {
	body := Render(ScopeSystem, "/usr/bin/pier")
	mustContain(t, body, "ExecStart=/usr/bin/pier serve")
	mustContain(t, body, "After=docker.service network-online.target")
	mustContain(t, body, "WantedBy=multi-user.target")
}

func TestParseScope(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Scope
	}{
		{"user", ScopeUser},
		{"system", ScopeSystem},
	} {
		got, err := ParseScope(tc.in)
		if err != nil {
			t.Fatalf("ParseScope(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseScope(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := ParseScope("nope"); err == nil {
		t.Fatal("ParseScope nope: expected error")
	}
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("body missing %q\n--body--\n%s", want, body)
	}
}

func mustNotContain(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("body unexpectedly contains %q\n--body--\n%s", unwanted, body)
	}
}
