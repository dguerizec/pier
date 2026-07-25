//go:build linux

package infra

import "testing"

func TestSelectHostDNSBackend(t *testing.T) {
	tests := []struct {
		name                string
		tailscaleResolvConf bool
		resolvedActive      bool
		tailscaleDNSActive  bool
		want                hostDNSBackend
	}{
		{
			name:                "tailscale owns resolv.conf",
			tailscaleResolvConf: true,
			resolvedActive:      true,
			tailscaleDNSActive:  true,
			want:                hostDNSTailscale,
		},
		{
			name:               "resolved wins over tailscale status",
			resolvedActive:     true,
			tailscaleDNSActive: true,
			want:               hostDNSSystemdResolved,
		},
		{
			name:               "tailscale platform integration",
			tailscaleDNSActive: true,
			want:               hostDNSTailscale,
		},
		{
			name: "manual fallback",
			want: hostDNSManual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectHostDNSBackend(
				tt.tailscaleResolvConf,
				tt.resolvedActive,
				tt.tailscaleDNSActive,
			)
			if got != tt.want {
				t.Fatalf("selectHostDNSBackend() = %v, want %v", got, tt.want)
			}
		})
	}
}
