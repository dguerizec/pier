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

[[expose]]
service = "app"
port    = 3000

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
	if m.Stack.Kind != KindCompose || m.Stack.File != "docker-compose.dev.yml" {
		t.Errorf("stack = %+v", m.Stack)
	}
	if len(m.Expose) != 1 || m.Expose[0].Service != "app" || m.Expose[0].Port != 3000 {
		t.Errorf("expose = %+v", m.Expose)
	}
	if d := m.DefaultExpose(); d == nil || d.Service != "app" {
		t.Errorf("default expose = %+v", d)
	}
	if len(m.Materialize.Symlinks) != 2 || m.Materialize.Symlinks[0] != ".env" {
		t.Errorf("symlinks = %v", m.Materialize.Symlinks)
	}
}

func TestLoad_MultiExpose(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "w3t"
base_domain = "w3t.test"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "front"

[[expose]]
service = "front"
port    = 8080

[[expose]]
service = "api"
port    = 8000
host    = "backend"
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Expose) != 2 {
		t.Fatalf("expose len = %d, want 2", len(m.Expose))
	}
	if m.Expose[1].Hostname() != "backend" {
		t.Errorf("expose[1].Hostname = %q, want backend", m.Expose[1].Hostname())
	}
	if m.Expose[0].Hostname() != "front" {
		t.Errorf("expose[0].Hostname = %q (default = service), want front", m.Expose[0].Hostname())
	}
	if d := m.DefaultExpose(); d == nil || d.Service != "front" {
		t.Errorf("default expose = %+v", d)
	}
}

func TestValidate_BaseDomainOptional(t *testing.T) {
	m := Manifest{
		Project: Project{Name: "x"},
		Stack:   Stack{Kind: KindCompose, File: "a"},
		Expose:  []ExposeRule{{Service: "a", Port: 1}},
	}
	if err := m.Validate(); err != nil {
		t.Errorf("unset base_domain should validate (composed at runtime), got %v", err)
	}
}

func TestDefaultExpose_NoMatch(t *testing.T) {
	m := Manifest{
		Project: Project{Name: "x", BaseDomain: "x.test"},
		Stack:   Stack{Kind: KindCompose, File: "a", Service: "ghost"},
		Expose:  []ExposeRule{{Service: "front", Port: 80}},
	}
	if d := m.DefaultExpose(); d != nil {
		t.Errorf("default expose = %+v, want nil (Stack.Service points at missing entry)", d)
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

[[expose]]
service = "app"
port    = 3000
`)
	writeFile(t, filepath.Join(dir, LocalFileName), `
[[expose]]
service = "app"
port    = 4000
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Expose[0].Port != 4000 {
		t.Errorf("port = %d, want 4000 (override)", m.Expose[0].Port)
	}
}

func TestValidate_Errors(t *testing.T) {
	okExpose := []ExposeRule{{Service: "app", Port: 3000}}
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{
			"missing name",
			Manifest{Project: Project{BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: okExpose},
			"project.name",
		},
		{
			"invalid name",
			Manifest{Project: Project{Name: "My_App", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: okExpose},
			"DNS label",
		},
		{
			"missing kind",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Expose: okExpose},
			"stack.kind",
		},
		{
			"unknown kind",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: "bogus"}, Expose: okExpose},
			"must be compose",
		},
		{
			"compose without file",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose}, Expose: okExpose},
			"stack.file",
		},
		{
			"dockerfile without dockerfile",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindDockerfile}, Expose: okExpose},
			"stack.dockerfile",
		},
		{
			"no expose",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}},
			"[[expose]]",
		},
		{
			"expose missing service",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Port: 1}}},
			"expose[0].service",
		},
		{
			"expose duplicate service",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 1}, {Service: "a", Port: 2}}},
			"listed twice",
		},
		{
			"expose duplicate host",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 1, Host: "shared"}, {Service: "b", Port: 2, Host: "shared"}}},
			"host \"shared\"",
		},
		{
			"expose bad port",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 0}}},
			"expose[0].port",
		},
		{
			"expose bad host",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 1, Host: "Bad_Host"}}},
			"is not a valid DNS label",
		},
		{
			"watch.on_change bogus",
			Manifest{
				Project: Project{Name: "x", BaseDomain: "x.test"},
				Stack:   Stack{Kind: KindCompose, File: "a"},
				Expose:  okExpose,
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
		Stack:   Stack{Kind: KindCompose, File: "docker-compose.yml", Service: "app"},
		Expose:  []ExposeRule{{Service: "app", Port: 3000}},
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
	if len(loaded.Expose) != 1 || loaded.Expose[0] != original.Expose[0] {
		t.Errorf("expose round-trip mismatch:\noriginal=%+v\nloaded=  %+v", original.Expose, loaded.Expose)
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
