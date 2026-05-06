package infra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDashboardRoute_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")

	path, err := WriteDashboardRoute(dir, "pier.test", "http://10.10.6.1:60080")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"Host(`pier.test`)",
		"http://10.10.6.1:60080",
		"pier-dashboard",
		"web",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Re-write with a different upstream → file overwritten in place.
	if _, err := WriteDashboardRoute(dir, "pier.test", "http://10.0.0.1:60080"); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), "10.10.6.1") {
		t.Error("re-write left old upstream")
	}
	if !strings.Contains(string(body), "10.0.0.1") {
		t.Error("re-write missing new upstream")
	}
}

func TestWriteDashboardRoute_AcceptsBaseDomainFQDN(t *testing.T) {
	// Dashboard FQDN under headscale base_domain (e.g. "pier.nebula")
	// must round-trip cleanly — no host/tld split forced by the API.
	dir := filepath.Join(t.TempDir(), "dynamic")
	path, err := WriteDashboardRoute(dir, "pier.nebula", "http://100.64.0.10:60080")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "Host(`pier.nebula`)") {
		t.Errorf("rule missing pier.nebula:\n%s", body)
	}
}

func TestWriteDashboardRoute_RejectsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")
	cases := []struct{ fqdn, upstream string }{
		{"", "http://x:1"},
		{"pier.test", ""},
	}
	for _, c := range cases {
		if _, err := WriteDashboardRoute(dir, c.fqdn, c.upstream); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestRemoveDashboardRoute_MissingIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")
	removed, err := RemoveDashboardRoute(dir)
	if err != nil {
		t.Errorf("removing missing route should be a no-op: %v", err)
	}
	if removed {
		t.Errorf("expected removed=false when route file is absent")
	}
}

func TestRemoveDashboardRoute_DeletesFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")
	path, err := WriteDashboardRoute(dir, "pier.test", "http://x:1")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveDashboardRoute(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Errorf("expected removed=true after deleting an existing route")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}
