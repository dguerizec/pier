package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTraefikDynamicDir_FlagEquals(t *testing.T) {
	got := extractTraefikDynamicDir(
		[]string{"traefik", "--api.insecure=true", "--providers.file.directory=/etc/traefik/dynamic"},
		identity,
	)
	if got != "/etc/traefik/dynamic" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTraefikDynamicDir_FlagSpaceSeparated(t *testing.T) {
	got := extractTraefikDynamicDir(
		[]string{"--providers.file.directory", "/srv/traefik/dyn", "--api.insecure"},
		identity,
	)
	if got != "/srv/traefik/dyn" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTraefikDynamicDir_FromConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "traefik.yml")
	body := "providers:\n  file:\n    directory: /opt/traefik/dynamic\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := extractTraefikDynamicDir(
		[]string{"--configFile", cfg},
		identity,
	)
	if got != "/opt/traefik/dynamic" {
		t.Errorf("got %q", got)
	}

	// Lowercase variant + `=` form.
	got = extractTraefikDynamicDir(
		[]string{"--configfile=" + cfg},
		identity,
	)
	if got != "/opt/traefik/dynamic" {
		t.Errorf("lowercase variant: got %q", got)
	}
}

func TestExtractTraefikDynamicDir_FilenameModeIgnored(t *testing.T) {
	// filename: pier can't drop a sibling file in single-file mode, so
	// detection must not return the parent dir as if it were a watched
	// directory. Wizard prompts the user instead.
	got := extractTraefikDynamicDir(
		[]string{"--providers.file.filename=/etc/traefik/single.yml"},
		identity,
	)
	if got != "" {
		t.Errorf("filename mode should not yield a dir, got %q", got)
	}
}

func TestExtractTraefikDynamicDir_ResolvePathApplied(t *testing.T) {
	// Simulate a docker container whose /etc/traefik is bind-mounted
	// from /home/user/traefik on the host.
	resolve := func(p string) string {
		if p == "/etc/traefik/dynamic" {
			return "/home/user/traefik/dynamic"
		}
		return p
	}
	got := extractTraefikDynamicDir(
		[]string{"--providers.file.directory=/etc/traefik/dynamic"},
		resolve,
	)
	if got != "/home/user/traefik/dynamic" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTraefikDynamicDir_ConfigFileMissing(t *testing.T) {
	got := extractTraefikDynamicDir(
		[]string{"--configFile=/no/such/file.yml"},
		identity,
	)
	if got != "" {
		t.Errorf("missing config file should yield empty, got %q", got)
	}
}

func identity(p string) string { return p }
