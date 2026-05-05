package initwizard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/LeoPartt/pier/internal/manifest"
)

type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// TestMain isolates the test process from the developer's real
// ~/.config/pier/prefs.toml so worktree-dir resolution falls back to
// the built-in default rather than picking up local state.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "pier-prefs-")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv("XDG_CONFIG_HOME", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func tomlDecodeFile(path string, v any) (toml.MetaData, error) { return toml.DecodeFile(path, v) }

func writeCompose(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "docker-compose.dev.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDerive_SingleService(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)

	p, ambig, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	// AmbMatchHostUID always fires when --[no-]match-host-uid wasn't passed;
	// the wizard wants to confirm the default with the user. Pin it via
	// Opts to silence in non-prompt scenarios — tested separately below.
	if len(ambig) != 1 || ambig[0].Kind != AmbMatchHostUID {
		t.Errorf("ambig = %+v, want only AmbMatchHostUID for single-service", ambig)
	}
	if !p.MatchHostUID {
		t.Error("MatchHostUID defaults to true on fresh init")
	}
	if p.Name != filepath.Base(dir) {
		// dir name may include random suffix but should be valid
		if err := ValidateName(p.Name); err != nil {
			t.Errorf("name %q invalid: %v", p.Name, err)
		}
	}
	if p.Domain != p.Name+".{pier.tld}" {
		t.Errorf("domain = %q", p.Domain)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Service != "app" {
		t.Errorf("candidates = %+v", p.Candidates)
	}
	if !p.Selected[0] {
		t.Error("single service should be selected by default")
	}
	if p.DefaultService != "app" {
		t.Errorf("default service = %q, want app", p.DefaultService)
	}
	if p.WorktreeDir != ".pier/worktrees" {
		t.Errorf("worktree dir = %q", p.WorktreeDir)
	}
	if !p.Share {
		t.Error("share defaults to true unless --private")
	}
}

func TestDerive_MultiService_FlagsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
  web:
    image: y
    ports: ["80:80"]
`)

	_, ambig, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[AmbiguityKind]bool{}
	for _, a := range ambig {
		kinds[a.Kind] = true
	}
	if !kinds[AmbExpose] {
		t.Error("expected AmbExpose flagged")
	}
	if !kinds[AmbDefaultService] {
		t.Error("expected AmbDefaultService flagged")
	}
}

func TestDerive_MultiService_ServiceFlagSilencesDefault(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
  web:
    image: y
    ports: ["80:80"]
`)

	_, ambig, err := Derive(dir, Opts{Service: "api"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ambig {
		if a.Kind == AmbDefaultService {
			t.Error("--service should silence the default-service ambiguity")
		}
	}
}

func TestDerive_MatchHostUIDOptsPinningSilencesPrompt(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)

	// nil Opts.MatchHostUID → ambiguity fires, default true.
	_, ambig, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	wantAmb := false
	for _, a := range ambig {
		if a.Kind == AmbMatchHostUID {
			wantAmb = true
		}
	}
	if !wantAmb {
		t.Error("nil Opts.MatchHostUID should fire AmbMatchHostUID")
	}

	// Pinned false → no ambiguity, value forced.
	no := false
	p, ambig, err := Derive(dir, Opts{MatchHostUID: &no})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ambig {
		if a.Kind == AmbMatchHostUID {
			t.Error("pinned Opts.MatchHostUID must silence AmbMatchHostUID")
		}
	}
	if p.MatchHostUID {
		t.Error("Opts.MatchHostUID=&false should pin Plan.MatchHostUID to false")
	}
}

func TestDerive_ReinitLegacyManifestDefaultsMatchHostUIDTrue(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	// Legacy manifest that pre-dates match_host_uid: the key is absent,
	// not explicitly false. The wizard must NOT inherit the parsed zero
	// value — it should fall through to the safe default (true) so the
	// re-init prompt has the right pre-selection.
	legacy := `[project]
name = "legacy"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "app"

[[expose]]
service = "app"
port = 3000
`
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.MatchHostUID {
		t.Error("legacy manifest (no match_host_uid key) must default to true, not the parsed zero value")
	}
}

func TestDerive_ReinitExplicitFalseSurvives(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	// Manifest with the key present and explicitly false: re-init must
	// preserve the user's choice rather than silently flip back to true.
	body := `[project]
name = "pin"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "app"
match_host_uid = false

[[expose]]
service = "app"
port = 3000
`
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if p.MatchHostUID {
		t.Error("explicit match_host_uid = false must survive re-init")
	}
}

func TestDerive_ReinitLoadsExisting(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
  web:
    image: y
    ports: ["80:80"]
`)
	existingTOML := `[project]
name = "myproj"
base_domain = "myproj.example"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "web"
match_host_uid = true

[[expose]]
service = "web"
port = 80
host = "frontend"

[worktree]
dir = "trees"
base_ref = "develop"

[env.web]
API_URL = "{url.api}"
`
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(existingTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatalf("re-init Derive: %v", err)
	}
	if !p.IsReinit() {
		t.Fatal("expected IsReinit true")
	}
	if p.Name != "myproj" {
		t.Errorf("name should come from existing, got %q", p.Name)
	}
	if p.Domain != "myproj.example" {
		t.Errorf("domain should come from existing, got %q", p.Domain)
	}
	if p.DefaultService != "web" {
		t.Errorf("default service should come from existing, got %q", p.DefaultService)
	}
	if p.WorktreeDir != "trees" || p.BaseRef != "develop" {
		t.Errorf("worktree should come from existing: %+v", p)
	}
	// Selection mirrors existing exposes only — "api" is a candidate but
	// wasn't previously exposed, so it should not be pre-selected.
	for i, c := range p.Candidates {
		if c.Service == "api" && p.Selected[i] {
			t.Error("api should not be pre-selected on re-init (not in existing)")
		}
		if c.Service == "web" && !p.Selected[i] {
			t.Error("web should be pre-selected on re-init")
		}
	}
	// Existing customisations preserved on the rule.
	rules := p.SelectedExposes()
	if len(rules) != 1 || rules[0].Host != "frontend" {
		t.Errorf("custom host not preserved: %+v", rules)
	}
	// match_host_uid kept on Existing.
	if !p.Existing.Stack.MatchHostUID {
		t.Error("match_host_uid should survive on Existing")
	}
	// Plan.MatchHostUID inherits the existing value when no flag is passed.
	if !p.MatchHostUID {
		t.Error("Plan.MatchHostUID should inherit existing manifest value on re-init")
	}
	// env.web kept on Existing.
	if v := p.Existing.Env["web"]["API_URL"]; v != "{url.api}" {
		t.Errorf("env.web preserved? got %q", v)
	}
}

func TestApply_ReinitPreservesUserSections(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
`)
	existingTOML := `[project]
name = "myproj"
base_domain = "myproj.example"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "api"

[[expose]]
service = "api"
port = 8000

[materialize]
symlinks = [".env"]
snapshots = ["data/"]

[hooks]
pre_up = "echo hi"

[env.api]
SECRET = "shh"
`
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(existingTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	var out testWriter
	if err := Apply(p, &out); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Read back and verify the user sections survived.
	roundtrip := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), roundtrip); err != nil {
		t.Fatal(err)
	}
	if len(roundtrip.Materialize.Symlinks) != 1 || roundtrip.Materialize.Symlinks[0] != ".env" {
		t.Errorf("symlinks lost: %+v", roundtrip.Materialize)
	}
	if len(roundtrip.Materialize.Snapshots) != 1 {
		t.Errorf("snapshots lost: %+v", roundtrip.Materialize)
	}
	if roundtrip.Hooks.PreUp != "echo hi" {
		t.Errorf("hooks lost: %+v", roundtrip.Hooks)
	}
	if roundtrip.Env["api"]["SECRET"] != "shh" {
		t.Errorf("env.api lost: %+v", roundtrip.Env)
	}
}

func TestDerive_NoPublishedPorts(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
`)
	if _, _, err := Derive(dir, Opts{}); err == nil {
		t.Error("expected error when no service has published ports")
	}
}

func TestDerive_PrivateDisablesShare(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	p, _, err := Derive(dir, Opts{Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Share {
		t.Error("--private should set Share=false")
	}
}

func TestDerive_FlagsHonored(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	p, _, err := Derive(dir, Opts{
		Name:        "myapp",
		Domain:      "myapp.example",
		WorktreeDir: "trees",
		BaseRef:     "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "myapp" || p.Domain != "myapp.example" {
		t.Errorf("name/domain not honoured: %+v", p)
	}
	if p.WorktreeDir != "trees" || p.BaseRef != "develop" {
		t.Errorf("worktree/base not honoured: %+v", p)
	}
}

func TestApply_FreshInit_NoWorktreeDirInManifest(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	var w testWriter
	if err := Apply(p, &w); err != nil {
		t.Fatal(err)
	}
	rt := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt); err != nil {
		t.Fatal(err)
	}
	if rt.Worktree.Dir != "" {
		t.Errorf("fresh init should not pin worktree.dir in the manifest, got %q", rt.Worktree.Dir)
	}
}

func TestApply_PreservesProjectWorktreeDirOnReinit(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(`
[project]
name = "p"
[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "app"
[[expose]]
service = "app"
port = 3000
[worktree]
dir = "./project-pinned"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	var w testWriter
	if err := Apply(p, &w); err != nil {
		t.Fatal(err)
	}
	rt := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt); err != nil {
		t.Fatal(err)
	}
	if rt.Worktree.Dir != "./project-pinned" {
		t.Errorf("project pin lost on re-init: %q", rt.Worktree.Dir)
	}
}

func TestApply_PersistsExplicitFlagToPrefs(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	p, _, err := Derive(dir, Opts{WorktreeDir: "/tmp/custom-worktrees"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.WorktreeDirExplicit {
		t.Fatal("WorktreeDirExplicit should be true when --worktree-dir is set")
	}
	var w testWriter
	if err := Apply(p, &w); err != nil {
		t.Fatal(err)
	}
	rt := &manifest.Manifest{}
	if _, err := tomlDecodeFile(filepath.Join(dir, ".pier.toml"), rt); err != nil {
		t.Fatal(err)
	}
	if rt.Worktree.Dir != "" {
		t.Errorf("--worktree-dir should not write to manifest, got %q", rt.Worktree.Dir)
	}
	// Prefs file should have been created with the new value.
	got := loadPrefsWorktreeDir()
	if got != "/tmp/custom-worktrees" {
		t.Errorf("prefs not persisted: got %q", got)
	}
}

func TestSelectedExposes_DefaultAll(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  api:
    image: x
    ports: ["8000:8000"]
  web:
    image: y
    ports: ["80:80"]
`)
	p, _, err := Derive(dir, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	rules := p.SelectedExposes()
	if len(rules) != 2 {
		t.Errorf("default selection should include all candidates, got %+v", rules)
	}
}
