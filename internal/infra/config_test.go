package infra

import "testing"

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
