package initwizard

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if len(ambig) != 0 {
		t.Errorf("ambig = %+v, want none for single-service", ambig)
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

func TestDerive_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, `services:
  app:
    image: x
    ports: ["3000:3000"]
`)
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Derive(dir, Opts{}); err == nil {
		t.Error("expected error when .pier.toml exists")
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
