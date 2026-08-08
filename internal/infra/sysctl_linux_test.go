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

// TestRenderNonlocalBindSysctl locks the emitted settings so changes remain
// deliberate even though install and doctor compare their effective values.
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

func TestNonlocalBindSysctlCurrentIgnoresCommentsAndFormatting(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "rendered config",
			body: string(renderNonlocalBindSysctl()),
			want: true,
		},
		{
			name: "legacy comments",
			body: `# Written by pier
# Allows bind() to non-local IPs so docker-proxy can bind the
# tailscale IP at boot before tailscaled has assigned it.
net.ipv4.ip_nonlocal_bind = 1
net.ipv6.ip_nonlocal_bind = 1
`,
			want: true,
		},
		{
			name: "alternate formatting and inline comments",
			body: `net.ipv4.ip_nonlocal_bind=1 # IPv4
net.ipv6.ip_nonlocal_bind = 1 ; IPv6
`,
			want: true,
		},
		{
			name: "missing IPv6",
			body: "net.ipv4.ip_nonlocal_bind = 1\n",
		},
		{
			name: "wrong value",
			body: `net.ipv4.ip_nonlocal_bind = 1
net.ipv6.ip_nonlocal_bind = 0
`,
		},
		{
			name: "last assignment wins",
			body: `net.ipv4.ip_nonlocal_bind = 1
net.ipv6.ip_nonlocal_bind = 1
net.ipv4.ip_nonlocal_bind = 0
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nonlocalBindSysctlCurrent([]byte(tt.body)); got != tt.want {
				t.Fatalf("current = %v, want %v for:\n%s", got, tt.want, tt.body)
			}
		})
	}
}
