package infra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDashboardRoute_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths := &Paths{TraefikDynamic: filepath.Join(dir, "dynamic")}

	path, err := WriteDashboardRoute(paths, "pier", "test", "http://host.docker.internal:60080")
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
		"http://host.docker.internal:60080",
		"pier-dashboard",
		"web",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Re-write with a different upstream → file overwritten in place.
	if _, err := WriteDashboardRoute(paths, "pier", "test", "http://10.0.0.1:60080"); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), "host.docker.internal") {
		t.Error("re-write left old upstream")
	}
	if !strings.Contains(string(body), "10.0.0.1") {
		t.Error("re-write missing new upstream")
	}
}

func TestWriteDashboardRoute_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	paths := &Paths{TraefikDynamic: filepath.Join(dir, "dynamic")}
	cases := []struct{ host, tld, upstream string }{
		{"", "test", "http://x:1"},
		{"pier", "", "http://x:1"},
		{"pier", "test", ""},
	}
	for _, c := range cases {
		if _, err := WriteDashboardRoute(paths, c.host, c.tld, c.upstream); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestRemoveDashboardRoute_MissingIsNoop(t *testing.T) {
	dir := t.TempDir()
	paths := &Paths{TraefikDynamic: filepath.Join(dir, "dynamic")}
	if err := RemoveDashboardRoute(paths); err != nil {
		t.Errorf("removing missing route should be a no-op: %v", err)
	}
}

func TestRemoveDashboardRoute_DeletesFile(t *testing.T) {
	dir := t.TempDir()
	paths := &Paths{TraefikDynamic: filepath.Join(dir, "dynamic")}
	path, err := WriteDashboardRoute(paths, "pier", "test", "http://x:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveDashboardRoute(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}
