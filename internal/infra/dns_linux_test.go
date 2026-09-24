//go:build linux

package infra

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestVerifiedTailscaleRoute(t *testing.T) {
	for _, tt := range []struct {
		name, domains   string
		address         []int
		fail, malformed bool
		want            bool
	}{
		{name: "matching split route", domains: "Link 3 (tailscale0): ~test", address: []int{100, 101, 196, 104}, want: true},
		{name: "search route", domains: "Link 3 (tailscale0): test", address: []int{100, 101, 196, 104}, want: true},
		{name: "different installation", domains: "~test", address: []int{100, 101, 196, 105}},
		{name: "suffix is not the route", domains: "~lap.test", address: []int{100, 101, 196, 104}},
		{name: "no route", domains: "~ts.net"},
		{name: "offline at login", domains: "~test", fail: true},
		{name: "unsupported JSON or invalid response", domains: "~test", malformed: true},
		{name: "invalid address", domains: "~test", address: []int{356, 101, 196, 104}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			run := func(args ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					if !reflect.DeepEqual(args, []string{"domain", "tailscale0"}) {
						t.Fatalf("unexpected route command: %v", args)
					}
					return []byte(tt.domains), nil
				}
				if !reflect.DeepEqual(args[:5], []string{"--json=short", "--cache=no", "--type=A", "--interface=tailscale0", "query"}) {
					t.Fatalf("probe must bypass cache and global route: %v", args)
				}
				if tt.fail {
					return nil, errors.New("network unavailable")
				}
				if tt.malformed {
					return []byte("invalid JSON"), nil
				}
				return json.Marshal(map[string]any{
					"key":     map[string]any{"class": 1, "type": 1, "name": args[5]},
					"address": tt.address,
				})
			}
			if got := verifiedTailscaleRoute("test", "100.101.196.104", run); got != tt.want {
				t.Fatalf("verified route = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigureHostDNSRemovesOnlyManagedRedundantDropin(t *testing.T) {
	for _, tt := range []struct {
		name, body      string
		exists, wantErr bool
		wantCalls       int
	}{
		{name: "old global dropin", body: string(renderResolvedDropin("test", "100.101.196.104")), exists: true, wantCalls: 2},
		{name: "already removed"},
		{name: "custom file preserved", body: "[Resolve]\nDNS=192.0.2.1\n", exists: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pier.conf")
			if tt.exists {
				if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls [][]string
			sudo := func(args ...string) error {
				calls = append(calls, args)
				if args[0] == "rm" {
					return os.Remove(args[2])
				}
				return nil
			}
			changed, err := configureHostDNSAt("test", "100.101.196.104", path, hostDNSVerifiedTailscale, sudo)
			if (err != nil) != tt.wantErr || changed != (tt.wantCalls > 0) || len(calls) != tt.wantCalls {
				t.Fatalf("changed=%v err=%v calls=%v", changed, err, calls)
			}
			if len(calls) == 2 && !reflect.DeepEqual(calls[1], []string{"systemctl", "reload-or-restart", "systemd-resolved"}) {
				t.Fatalf("must reload after removal: %v", calls)
			}
			if !tt.wantErr {
				changed, err = configureHostDNSAt("test", "100.101.196.104", path, hostDNSVerifiedTailscale, sudo)
				if changed || err != nil || len(calls) != tt.wantCalls {
					t.Fatal("reconciliation is not idempotent")
				}
			}
		})
	}
}

func TestConfigureHostDNSKeepsDropinWithoutVerifiedRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pier.conf")
	body := renderResolvedDropin("test", "100.101.196.104")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := configureHostDNSAt("test", "100.101.196.104", path, hostDNSSystemdResolved,
		func(...string) error { t.Fatal("unchanged fallback must not invoke sudo"); return nil })
	if changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestConfigureHostDNSRemovalFailureStopsReconciliation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pier.conf")
	if err := os.WriteFile(path, renderResolvedDropin("test", "100.101.196.104"), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("permission denied")
	calls := 0
	changed, err := configureHostDNSAt("test", "100.101.196.104", path, hostDNSVerifiedTailscale,
		func(...string) error { calls++; return failure })
	if changed || !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("changed=%v err=%v calls=%d", changed, err, calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed removal must preserve drop-in:", err)
	}
}
