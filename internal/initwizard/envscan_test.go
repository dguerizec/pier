package initwizard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeoPartt/pier/internal/manifest"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanEnvSuggestions_DirectHostname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  api:
    ports: ["8000:8000"]
  front:
    ports: ["8080:8080"]
    environment:
      API_URL: "http://api:8000/v1"
      LOCAL: "http://localhost"
      OTHER: "static-string"
`)
	got := ScanEnvSuggestions(path, nil)
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Service != "front" || s.Key != "API_URL" || s.Target != "api" {
		t.Errorf("unexpected suggestion: %+v", s)
	}
	if s.Replacement != "{url.api}/v1" {
		t.Errorf("replacement = %q, want {url.api}/v1", s.Replacement)
	}
}

func TestScanEnvSuggestions_LoopbackViaHostPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  api:
    ports: ["60181:8000"]
  front:
    ports: ["60180:8080"]
    environment:
      API_PUBLIC_URL: "http://localhost:60181"
`)
	got := ScanEnvSuggestions(path, nil)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Target != "api" || got[0].Replacement != "{url.api}" {
		t.Errorf("unexpected: %+v", got[0])
	}
}

func TestScanEnvSuggestions_SelfReferenceSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  api:
    ports: ["8000:8000"]
    environment:
      SELF: "http://api:8000"
`)
	if got := ScanEnvSuggestions(path, nil); len(got) != 0 {
		t.Errorf("self-reference should be skipped, got %+v", got)
	}
}

func TestScanEnvSuggestions_ListForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  api:
    ports: ["8000:8000"]
  front:
    ports: ["8080:8080"]
    environment:
      - "API_URL=http://api:8000"
      - "BARE_FLAG"
      - "STATIC=hello"
`)
	got := ScanEnvSuggestions(path, nil)
	if len(got) != 1 || got[0].Key != "API_URL" {
		t.Errorf("list form parse: %+v", got)
	}
}

func TestScanEnvSuggestions_SkipsOverridden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  api:
    ports: ["8000:8000"]
  front:
    ports: ["8080:8080"]
    environment:
      API_URL: "http://api:8000"
`)
	existing := &manifest.Manifest{
		Env: map[string]map[string]string{
			"front": {"API_URL": "{url.api}"},
		},
	}
	if got := ScanEnvSuggestions(path, existing); len(got) != 0 {
		t.Errorf("already-overridden key should be skipped, got %+v", got)
	}
}

func TestParseHostPort(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"8080:80", 8080},
		{"127.0.0.1:8080:80", 8080},
		{"${PORT:-8080}:80", 8080},
		{"8080:80/tcp", 8080},
		{"3000", 0},
		{3000, 0},
		{map[string]any{"published": 8080, "target": 80}, 8080},
		{map[string]any{"published": "8080", "target": 80}, 8080},
		{map[string]any{"target": 80}, 0},
		{"${PORT}:80", 0},
	}
	for _, c := range cases {
		if got := parseHostPort(c.in); got != c.want {
			t.Errorf("parseHostPort(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestApply_WritesAcceptedEnv(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
  front:
    image: y
    ports: ["8080:8080"]
    environment:
      API_URL: "http://api:8000/v1"
`)
	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.EnvSuggestions) != 1 {
		t.Fatalf("expected 1 suggestion, got %+v", p.EnvSuggestions)
	}
	// Default --yes path: nothing accepted, env stays empty.
	var w testWriter
	if err := Apply(p, &w); err != nil {
		t.Fatal(err)
	}
	rt := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt); err != nil {
		t.Fatal(err)
	}
	if rt.Env["front"]["API_URL"] != "" {
		t.Errorf("--yes default should not write env, got %+v", rt.Env)
	}

	// Now simulate the user accepting the suggestion.
	os.Remove(filepath.Join(dir, ".pier.toml"))
	p2, _, _ := Derive(dir, Opts{})
	p2.EnvAccepted[0] = true
	if err := Apply(p2, &w); err != nil {
		t.Fatal(err)
	}
	rt2 := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt2); err != nil {
		t.Fatal(err)
	}
	if rt2.Env["front"]["API_URL"] != "{url.api}/v1" {
		t.Errorf("expected templated value, got %+v", rt2.Env)
	}
}
