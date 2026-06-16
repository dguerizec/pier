package cli

import (
	"strings"
	"testing"

	"github.com/dguerizec/pier/internal/detect"
)

func TestValidateDashboardFQDN(t *testing.T) {
	envHS := detect.Environment{
		Headscale: detect.HeadscaleInfo{
			Found:       true,
			BaseDomain:  "nebula",
			Container:   "headscale",
			RecordsPath: "/etc/headscale/dns_records.json",
		},
	}
	envHSNoRecords := detect.Environment{
		Headscale: detect.HeadscaleInfo{
			Found:      true,
			BaseDomain: "nebula",
			Container:  "headscale",
		},
	}
	envBare := detect.Environment{}

	t.Run("under TLD: no records adapter", func(t *testing.T) {
		fqdn, container, path, err := validateDashboardFQDN("pier.test", "test", envHS)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fqdn != "pier.test" {
			t.Errorf("fqdn = %q, want pier.test", fqdn)
		}
		if container != "" || path != "" {
			t.Errorf("expected no records adapter (got %q / %q)", container, path)
		}
	})

	t.Run("custom hostname under TLD", func(t *testing.T) {
		fqdn, _, _, err := validateDashboardFQDN("dash.test", "test", envHS)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fqdn != "dash.test" {
			t.Errorf("fqdn = %q, want dash.test", fqdn)
		}
	})

	t.Run("under base_domain with records: returns adapter info", func(t *testing.T) {
		fqdn, container, path, err := validateDashboardFQDN("pier.nebula", "test", envHS)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fqdn != "pier.nebula" {
			t.Errorf("fqdn = %q", fqdn)
		}
		if container != "headscale" || path != "/etc/headscale/dns_records.json" {
			t.Errorf("missing adapter info: container=%q path=%q", container, path)
		}
	})

	t.Run("under base_domain without records: error", func(t *testing.T) {
		_, _, _, err := validateDashboardFQDN("pier.nebula", "test", envHSNoRecords)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "extra_records_path") {
			t.Errorf("error should hint at extra_records_path, got: %v", err)
		}
	})

	t.Run("foreign domain: error", func(t *testing.T) {
		_, _, _, err := validateDashboardFQDN("dash.example.com", "test", envHS)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty: error", func(t *testing.T) {
		_, _, _, err := validateDashboardFQDN("", "test", envHS)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("no headscale at all: only TLD valid", func(t *testing.T) {
		fqdn, _, _, err := validateDashboardFQDN("dash.test", "test", envBare)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fqdn != "dash.test" {
			t.Errorf("fqdn = %q", fqdn)
		}
		if _, _, _, err := validateDashboardFQDN("dash.foreign.dev", "test", envBare); err == nil {
			t.Errorf("expected error for foreign domain when no headscale")
		}
	})
}
