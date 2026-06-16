//go:build linux

package infra

import (
	"strings"
	"testing"
)

// TestNeedsNonlocalBindForIP pins which bind IPs trigger the sysctl
// drop-in. Loopback and wildcard binds never need it (the kernel
// accepts them regardless of interface state); a specific routable
// IP does, because docker may try to bind it before tailscale brings
// the interface up.
func TestNeedsNonlocalBindForIP(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"garbage", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"100.101.196.104", true},
		{"192.168.1.42", true},
		{"fd7a:115c:a1e0::1", true},
	}
	for _, tc := range cases {
		got := needsNonlocalBindForIP(tc.in)
		if got != tc.want {
			t.Errorf("needsNonlocalBindForIP(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRenderNonlocalBindSysctl locks the file body so changes to the
// emitted sysctl are deliberate. doctor's content-match check compares
// byte-for-byte; tweaking whitespace silently turns the install into a
// stale-file recreate cycle on every `pier install`.
func TestRenderNonlocalBindSysctl(t *testing.T) {
	body := string(renderNonlocalBindSysctl())
	for _, want := range []string{
		"net.ipv4.ip_nonlocal_bind = 1",
		"net.ipv6.ip_nonlocal_bind = 1",
		"Written by pier",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered sysctl missing %q:\n%s", want, body)
		}
	}
}
