package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/detect"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/share"
)

func TestComposeInstallPlanDefaultsToLocalWithOptionalIntegrationsPresent(t *testing.T) {
	env := detect.Environment{
		Tailscale: detect.TailscaleInfo{
			Active:  true,
			IPv4:    "100.64.0.10",
			Tailnet: "example.ts.net",
		},
		Headscale: detect.HeadscaleInfo{
			Found:      true,
			Container:  "headscale",
			ConfigPath: "/srv/headscale/config.yaml",
			BaseDomain: "internal.example",
		},
	}

	got := composeInstallPlan(env, installOpts{}, installRouting{Reachability: reachabilityLocal})

	if got.Mode != infra.ModeLocal {
		t.Fatalf("mode = %q, want local", got.Mode)
	}
	if got.BindIP != "" || got.AnswerIP != "" {
		t.Fatalf("local plan unexpectedly exposes bind=%q answer=%q", got.BindIP, got.AnswerIP)
	}
	if got.HeadscaleConfigPath != "" || got.HeadscaleContainer != "" {
		t.Fatalf("local plan unexpectedly configures headscale: %+v", got)
	}
}

func TestSelectInstallRoutingYesKeepsLocalDefault(t *testing.T) {
	env := detect.Environment{
		Tailscale: detect.TailscaleInfo{
			Active: true,
			IPv4:   "100.64.0.10",
		},
		Headscale: detect.HeadscaleInfo{Found: true},
	}

	got, err := selectInstallRouting(&cobra.Command{}, env, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reachability != reachabilityLocal || got.IP != "" {
		t.Fatalf("routing = %+v, want local default", got)
	}
}

func TestComposeInstallPlanLANUsesSelectedAddressWithoutHeadscale(t *testing.T) {
	env := detect.Environment{
		Headscale: detect.HeadscaleInfo{
			Found:      true,
			Container:  "headscale",
			ConfigPath: "/srv/headscale/config.yaml",
			BaseDomain: "internal.example",
		},
	}

	got := composeInstallPlan(env, installOpts{}, installRouting{
		Reachability: reachabilityLAN,
		IP:           "192.168.1.42",
	})

	if got.Mode != infra.ModeServer {
		t.Fatalf("mode = %q, want server", got.Mode)
	}
	if got.BindIP != "192.168.1.42" || got.AnswerIP != "192.168.1.42" {
		t.Fatalf("LAN plan bind=%q answer=%q", got.BindIP, got.AnswerIP)
	}
	if got.HeadscaleConfigPath != "" || got.HeadscaleContainer != "" {
		t.Fatalf("LAN plan unexpectedly configures headscale: %+v", got)
	}
}

func TestComposeInstallPlanTailscaleEnablesDetectedHeadscaleIntegration(t *testing.T) {
	env := detect.Environment{
		Tailscale: detect.TailscaleInfo{
			Active:  true,
			IPv4:    "100.64.0.10",
			Tailnet: "example.ts.net",
		},
		Headscale: detect.HeadscaleInfo{
			Found:      true,
			Container:  "headscale",
			ConfigPath: "/srv/headscale/config.yaml",
			BaseDomain: "internal.example",
		},
	}

	got := composeInstallPlan(env, installOpts{}, installRouting{
		Reachability: reachabilityTailscale,
		IP:           env.Tailscale.IPv4,
	})

	if got.Mode != infra.ModeServer {
		t.Fatalf("mode = %q, want server", got.Mode)
	}
	if got.BindIP != "100.64.0.10" || got.AnswerIP != "100.64.0.10" {
		t.Fatalf("tailscale plan bind=%q answer=%q", got.BindIP, got.AnswerIP)
	}
	if got.HeadscaleConfigPath != "/srv/headscale/config.yaml" || got.HeadscaleContainer != "headscale" {
		t.Fatalf("tailscale plan did not retain headscale integration: %+v", got)
	}
}

func TestWithoutIPRemovesTailscaleFromLANAddresses(t *testing.T) {
	addresses := []share.Address{
		{Interface: "enp3s0", IP: "192.168.1.42"},
		{Interface: "tailscale0", IP: "100.64.0.10"},
	}

	got := withoutIP(addresses, "100.64.0.10")

	if len(got) != 1 || got[0].Interface != "enp3s0" {
		t.Fatalf("filtered addresses = %+v, want only enp3s0", got)
	}
}

func TestAvailableInstallReachabilitiesAlwaysIncludesLAN(t *testing.T) {
	got := availableInstallReachabilities(detect.Environment{})
	want := []installReachability{reachabilityLocal, reachabilityLAN}

	if len(got) != len(want) {
		t.Fatalf("reachabilities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reachabilities = %v, want %v", got, want)
		}
	}
}

func TestAvailableInstallReachabilitiesOnlyIncludesDetectedTailscale(t *testing.T) {
	withoutIPv4 := availableInstallReachabilities(detect.Environment{
		Tailscale: detect.TailscaleInfo{Active: true},
	})
	if len(withoutIPv4) != 2 {
		t.Fatalf("tailscale without IPv4 should not be offered: %v", withoutIPv4)
	}

	withIPv4 := availableInstallReachabilities(detect.Environment{
		Tailscale: detect.TailscaleInfo{Active: true, IPv4: "100.64.0.10"},
	})
	if len(withIPv4) != 3 || withIPv4[2] != reachabilityTailscale {
		t.Fatalf("detected tailscale should be offered after local and LAN: %v", withIPv4)
	}
}

func TestSelectInstallLANAddressExplainsMissingInterface(t *testing.T) {
	_, err := selectInstallLANAddress(nil)
	if err == nil {
		t.Fatal("expected missing LAN address error")
	}
	if want := "no active LAN IPv4 address found"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

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
