package infra

import "testing"

func TestConfigForInstallPreservesServeSettings(t *testing.T) {
	previous := &Config{
		DashboardFQDN:        "pier.example.net",
		HeadscaleRecordsPath: "/srv/headscale/records.json",
		HeadscaleConfigPath:  "/old/headscale.yaml",
	}

	got := configForInstall(InstallOptions{
		Mode:                ModeServer,
		TLD:                 "test",
		BindIP:              "100.64.0.10",
		AnswerIP:            "100.64.0.10",
		ManualDNS:           true,
		PreviousConfig:      previous,
		HeadscaleConfigPath: "/new/headscale.yaml",
	}, NetworkName)

	if got.DashboardFQDN != previous.DashboardFQDN || got.HeadscaleRecordsPath != previous.HeadscaleRecordsPath {
		t.Fatalf("serve settings were not preserved: %+v", got)
	}
	if got.HeadscaleConfigPath != "/new/headscale.yaml" {
		t.Fatalf("install-owned headscale path = %q, want new value", got.HeadscaleConfigPath)
	}
	if !got.ManualDNS {
		t.Fatal("manual DNS preference was not persisted")
	}
}

func TestEffectiveDashboardFQDN(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"explicit FQDN wins", Config{TLD: "test", DashboardFQDN: "pier.nebula"}, "pier.nebula"},
		{"default falls back to pier.<TLD>", Config{TLD: "test"}, "pier.test"},
		{"no TLD, no FQDN → empty", Config{}, ""},
		{"DashboardFQDN under TLD is also valid (explicit)", Config{TLD: "test", DashboardFQDN: "dash.test"}, "dash.test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.EffectiveDashboardFQDN(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
