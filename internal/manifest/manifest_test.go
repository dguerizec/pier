package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoad_Compose(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "myapp"
base_domain = "myapp.test"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "app"
port = 3000

[materialize]
symlinks  = [".env", "secrets/"]
snapshots = ["data-dev/"]
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Project.Name != "myapp" || m.Project.BaseDomain != "myapp.test" {
		t.Errorf("project = %+v", m.Project)
	}
	if m.Stack.Kind != KindCompose || m.Stack.File != "docker-compose.dev.yml" || m.Stack.Port != 3000 {
		t.Errorf("stack = %+v", m.Stack)
	}
	if len(m.Materialize.Symlinks) != 2 || m.Materialize.Symlinks[0] != ".env" {
		t.Errorf("symlinks = %v", m.Materialize.Symlinks)
	}
}

func TestLoad_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLoad_LocalOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "myapp"
base_domain = "myapp.test"

[stack]
kind = "compose"
file = "docker-compose.yml"
port = 3000
`)
	writeFile(t, filepath.Join(dir, LocalFileName), `
[stack]
port = 4000
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Stack.Port != 4000 {
		t.Errorf("port = %d, want 4000 (override from .pier.local.toml)", m.Stack.Port)
	}
	if m.Stack.File != "docker-compose.yml" {
		t.Errorf("file = %q, want docker-compose.yml (carried from base manifest)", m.Stack.File)
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{
			"missing name",
			Manifest{Project: Project{BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a", Port: 1}},
			"project.name",
		},
		{
			"invalid name",
			Manifest{Project: Project{Name: "My_App", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a", Port: 1}},
			"DNS label",
		},
		{
			"missing base_domain",
			Manifest{Project: Project{Name: "x"}, Stack: Stack{Kind: KindCompose, File: "a", Port: 1}},
			"base_domain",
		},
		{
			"missing kind",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}},
			"stack.kind",
		},
		{
			"unknown kind",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: "bogus"}},
			"must be compose",
		},
		{
			"compose without file",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, Port: 1}},
			"stack.file",
		},
		{
			"compose without port",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}},
			"stack.port",
		},
		{
			"dockerfile without dockerfile",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindDockerfile, Port: 1}},
			"stack.dockerfile",
		},
		{
			"watch.on_change bogus",
			Manifest{
				Project: Project{Name: "x", BaseDomain: "x.test"},
				Stack:   Stack{Kind: KindCompose, File: "a", Port: 1},
				Watch:   Watch{OnChange: "bogus"},
			},
			"on_change",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), c.want) {
				t.Errorf("err = %q, want substring %q", err.Error(), c.want)
			}
		})
	}
}

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &Manifest{
		Project: Project{Name: "myapp", BaseDomain: "myapp.test"},
		Stack:   Stack{Kind: KindCompose, File: "docker-compose.yml", Service: "app", Port: 3000},
		Materialize: Materialize{
			Symlinks:  []string{".env"},
			Snapshots: []string{"data-dev/"},
		},
	}
	path := filepath.Join(dir, FileName)
	if err := original.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Project != original.Project || loaded.Stack != original.Stack {
		t.Errorf("round-trip mismatch:\noriginal=%+v\nloaded=  %+v", original, loaded)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
