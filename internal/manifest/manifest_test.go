package manifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

[stack.env]
APP_HOST_PORT = "3210"

[[expose]]
service = "app"
port    = 3000
preserve_ports = [2223, 2224]

[service.worker]
match_host_uid = true

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
	if got := m.Stack.Env["APP_HOST_PORT"]; got != "3210" {
		t.Errorf("stack.env.APP_HOST_PORT = %q", got)
	}
	if len(m.Expose) != 1 || m.Expose[0].Service != "app" || m.Expose[0].Port != 3000 {
		t.Errorf("expose = %+v", m.Expose)
	}
	if len(m.Expose[0].PreservePorts) != 2 || m.Expose[0].PreservePorts[0] != 2223 || m.Expose[0].PreservePorts[1] != 2224 {
		t.Errorf("expose[0].preserve_ports = %v", m.Expose[0].PreservePorts)
	}
	if d := m.DefaultExpose(); d == nil || d.Service != "app" {
		t.Errorf("default expose = %+v", d)
	}
	if !m.Service["worker"].MatchHostUID {
		t.Errorf("service.worker.match_host_uid = false, want true")
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

func TestLoadResolved_ValueTemplates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "pickatube"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"

[stack.env]
PICKATUBE_OAUTH_RELAY_PORT = "{value.oauth_callback_port}"

[[expose]]
service = "backend"
port = 8000
preserve_ports = [{value.oauth_callback_port}]

[env.backend]
GOOGLE_OAUTH_REDIRECT_URI = "http://127.0.0.1:{value.oauth_callback_port}/oauth/google/callback"

[hooks]
resolve_values = "./scripts/resolve-pier-values"
`)
	bootstrap, err := LoadBootstrap(dir)
	if err != nil {
		t.Fatalf("LoadBootstrap: %v", err)
	}
	if bootstrap.Hooks.ResolveValues != "./scripts/resolve-pier-values" {
		t.Errorf("resolve_values = %q", bootstrap.Hooks.ResolveValues)
	}

	masked, err := Load(dir)
	if err != nil {
		t.Fatalf("Load bootstrap-safe manifest: %v", err)
	}
	if got := masked.Expose[0].PreservePorts[0]; got != 1 {
		t.Errorf("masked preserve port = %d, want 1", got)
	}

	resolved, err := LoadResolved(dir, map[string]any{
		"oauth_callback_port": json.Number("49163"),
	})
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if got := resolved.Expose[0].PreservePorts[0]; got != 49163 {
		t.Errorf("preserve port = %d, want 49163", got)
	}
	if got := resolved.Stack.Env["PICKATUBE_OAUTH_RELAY_PORT"]; got != "49163" {
		t.Errorf("stack env port = %q, want 49163", got)
	}
	if got := resolved.Env["backend"]["GOOGLE_OAUTH_REDIRECT_URI"]; got != "http://127.0.0.1:49163/oauth/google/callback" {
		t.Errorf("redirect URI = %q", got)
	}
}

func TestLoad_ValueTemplateRequiresResolver(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "demo"

[stack]
kind = "compose"
file = "compose.yml"

[[expose]]
service = "app"
port = 3000
preserve_ports = [{value.port}]
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "hooks.resolve_values") {
		t.Fatalf("err = %v, want resolver requirement", err)
	}
}

func TestLoadBootstrap_RejectsStaticValueTemplate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `
[project]
name = "demo"

[stack]
kind = "compose"
file = "compose.yml"

[[expose]]
service = "app"
port = 3000

[materialize]
snapshots = ["data-{value.suffix}"]

[hooks]
resolve_values = "./scripts/resolve-values"
`)
	_, err := LoadBootstrap(dir)
	if err == nil || !strings.Contains(err.Error(), "materialize cannot contain") {
		t.Fatalf("err = %v, want static materialize diagnostic", err)
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
			"invalid stack env name",
			Manifest{
				Project: Project{Name: "x", BaseDomain: "x.test"},
				Stack:   Stack{Kind: KindCompose, File: "a", Env: map[string]string{"BAD-NAME": "x"}},
				Expose:  okExpose,
			},
			"valid environment variable name",
		},
		{
			"reserved stack env name",
			Manifest{
				Project: Project{Name: "x", BaseDomain: "x.test"},
				Stack:   Stack{Kind: KindCompose, File: "a", Env: map[string]string{"PIER_SLUG": "x"}},
				Expose:  okExpose,
			},
			"reserved PIER_ prefix",
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
			"expose bad preserve port",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 1, PreservePorts: []int{0}}}},
			"preserve_ports",
		},
		{
			"expose duplicate preserve port",
			Manifest{Project: Project{Name: "x", BaseDomain: "x.test"}, Stack: Stack{Kind: KindCompose, File: "a"}, Expose: []ExposeRule{{Service: "a", Port: 1, PreservePorts: []int{2223, 2223}}}},
			"duplicate port",
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
		{
			"empty service override name",
			Manifest{
				Project: Project{Name: "x", BaseDomain: "x.test"},
				Stack:   Stack{Kind: KindCompose, File: "a"},
				Expose:  okExpose,
				Service: map[string]ServiceConfig{
					" ": {MatchHostUID: true},
				},
			},
			"service table name",
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
		Expose:  []ExposeRule{{Service: "app", Port: 3000, PreservePorts: []int{2223, 2224}}},
		Service: map[string]ServiceConfig{
			"worker": {MatchHostUID: true},
		},
		Materialize: Materialize{
			Symlinks:   []string{".env"},
			Snapshots:  []string{"data-dev/"},
			PostCreate: []string{"./scripts/seed.sh"},
			PreRemove:  []string{"./scripts/backup.sh"},
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
	if loaded.Project != original.Project || !reflect.DeepEqual(loaded.Stack, original.Stack) {
		t.Errorf("round-trip mismatch:\noriginal=%+v\nloaded=  %+v", original, loaded)
	}
	if len(loaded.Expose) != 1 ||
		loaded.Expose[0].Service != original.Expose[0].Service ||
		loaded.Expose[0].Port != original.Expose[0].Port ||
		loaded.Expose[0].Host != original.Expose[0].Host ||
		len(loaded.Expose[0].PreservePorts) != 2 ||
		loaded.Expose[0].PreservePorts[0] != 2223 ||
		loaded.Expose[0].PreservePorts[1] != 2224 {
		t.Errorf("expose round-trip mismatch:\noriginal=%+v\nloaded=  %+v", original.Expose, loaded.Expose)
	}
	if !loaded.Service["worker"].MatchHostUID {
		t.Errorf("service.worker.match_host_uid = false, want true")
	}
	if len(loaded.Materialize.PostCreate) != 1 || loaded.Materialize.PostCreate[0] != "./scripts/seed.sh" {
		t.Errorf("post_create round-trip mismatch: %v", loaded.Materialize.PostCreate)
	}
	if len(loaded.Materialize.PreRemove) != 1 || loaded.Materialize.PreRemove[0] != "./scripts/backup.sh" {
		t.Errorf("pre_remove round-trip mismatch: %v", loaded.Materialize.PreRemove)
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
