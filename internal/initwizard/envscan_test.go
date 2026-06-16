package initwizard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dguerizec/pier/internal/manifest"
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

func TestScanEnvVarPrompts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  app:
    ports: ["3000:3000"]
    environment:
      VITE_ALLOWED_HOSTS: ${VITE_ALLOWED_HOSTS-}
      VITE_API_TARGET: http://backend:14140
      DB_URL: ${DB_URL:-postgres://localhost/dev}
      ERR_VAR: ${MUST_BE_SET:?missing}
      INLINE: prefix-${THING-x}
      STATIC: hello
`)
	got := ScanEnvVarPrompts(path, nil)
	byKey := map[string]EnvVarPrompt{}
	for _, p := range got {
		byKey[p.Key] = p
	}

	if p, ok := byKey["VITE_ALLOWED_HOSTS"]; !ok {
		t.Errorf("missing VITE_ALLOWED_HOSTS prompt")
	} else if p.HostVar != "VITE_ALLOWED_HOSTS" || p.Default != "" {
		t.Errorf("unexpected: %+v", p)
	}
	if p, ok := byKey["DB_URL"]; !ok {
		t.Errorf("missing DB_URL prompt")
	} else if p.Default != "postgres://localhost/dev" {
		t.Errorf("DB_URL default: %q", p.Default)
	}
	if _, ok := byKey["VITE_API_TARGET"]; ok {
		t.Errorf("VITE_API_TARGET is a literal URL, should not be a prompt")
	}
	if _, ok := byKey["INLINE"]; ok {
		t.Errorf("partial interpolation should not be a prompt")
	}
	if _, ok := byKey["STATIC"]; ok {
		t.Errorf("static value should not be a prompt")
	}
	if p, ok := byKey["ERR_VAR"]; ok {
		// :? is diagnostic, not a default — Default must stay empty.
		if p.Default != "" {
			t.Errorf("ERR_VAR default should be empty, got %q", p.Default)
		}
	}
}

func TestScanEnvVarPrompts_SkipsOverridden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	writeFile(t, path, `services:
  app:
    ports: ["3000:3000"]
    environment:
      FOO: ${FOO-}
`)
	existing := &manifest.Manifest{
		Env: map[string]map[string]string{"app": {"FOO": "set"}},
	}
	if got := ScanEnvVarPrompts(path, existing); len(got) != 0 {
		t.Errorf("overridden key should be skipped, got %+v", got)
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

func TestApply_WritesFilledEnvVarPrompts(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
    environment:
      ALLOWED: ${ALLOWED-}
      OTHER: ${OTHER-}
`)
	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.EnvVarPrompts) != 2 {
		t.Fatalf("expected 2 prompts, got %+v", p.EnvVarPrompts)
	}
	// Fill ALLOWED, leave OTHER empty.
	for i, prompt := range p.EnvVarPrompts {
		if prompt.Key == "ALLOWED" {
			p.EnvVarValues[i] = "host1,host2"
		}
	}
	var w testWriter
	if err := Apply(p, &w); err != nil {
		t.Fatal(err)
	}
	rt := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt); err != nil {
		t.Fatal(err)
	}
	if rt.Env["app"]["ALLOWED"] != "host1,host2" {
		t.Errorf("ALLOWED not written: %+v", rt.Env)
	}
	if _, ok := rt.Env["app"]["OTHER"]; ok {
		t.Errorf("empty value should not be written, got %+v", rt.Env)
	}
}
