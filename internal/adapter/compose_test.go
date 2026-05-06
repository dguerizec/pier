package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/manifest"
)

func TestRenderOverride_SingleExpose(t *testing.T) {
	c := Ctx{
		Project:        "myapp",
		Slug:           "feat-x",
		BaseDomain:     "myapp.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    "docker-compose.yml",
			Service: "web",
		},
		Expose:         []manifest.ExposeRule{{Service: "web", Port: 3000}},
		DefaultService: "web",
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)

	want := []string{
		"container_name: myapp-feat-x-web",
		"traefik.enable=true",
		// primary router uses the per-service host
		"traefik.http.routers.myapp-feat-x-web.rule=Host(`web.feat-x.myapp.test`)",
		// alias router for the default service uses the bare slug
		"traefik.http.routers.myapp-feat-x-web-default.rule=Host(`feat-x.myapp.test`)",
		"traefik.http.routers.myapp-feat-x-web-default.service=myapp-feat-x-web",
		"traefik.docker.network=pier",
		"traefik.http.services.myapp-feat-x-web.loadbalancer.server.port=3000",
		"networks: [default, pier]",
		"  pier:",
		"    external: true",
	}
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}
}

func TestRenderOverride_MultiExposeNoAlias(t *testing.T) {
	c := Ctx{
		Project:        "w3t",
		Slug:           "x",
		BaseDomain:     "w3t.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
			// No Stack.Service → no alias
		},
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080},
			{Service: "api", Port: 8000, Host: "backend"},
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)

	for _, w := range []string{
		"traefik.http.routers.w3t-x-front.rule=Host(`front.x.w3t.test`)",
		"traefik.http.routers.w3t-x-api.rule=Host(`backend.x.w3t.test`)",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}
	if strings.Contains(s, "-default.rule=Host(") {
		t.Errorf("no service is default, alias router should not be rendered:\n%s", s)
	}
}

func TestRenderOverride_MatchHostUID(t *testing.T) {
	c := Ctx{
		Project:        "myapp",
		Slug:           "x",
		BaseDomain:     "myapp.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind:         manifest.KindCompose,
			File:         "docker-compose.yml",
			Service:      "web",
			MatchHostUID: true,
		},
		Expose:         []manifest.ExposeRule{{Service: "web", Port: 3000}},
		DefaultService: "web",
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)

	expected := fmt.Sprintf(`user: "%d:%d"`, os.Getuid(), os.Getgid())
	if !strings.Contains(s, expected) {
		t.Errorf("expected %q in override, got:\n%s", expected, s)
	}

	c.Stack.MatchHostUID = false
	got, err = renderOverride(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "user:") {
		t.Errorf("user: line should be absent when MatchHostUID is false, got:\n%s", got)
	}
}

func TestRenderOverride_StripsHostBindings(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  front:
    image: node:20-alpine
    container_name: web3tiers-front
    ports:
      - "60180:8080"
  api:
    image: python:3.12-slim
    container_name: web3tiers-api
    ports:
      - "60181:8000"
  redis:
    image: redis:7-alpine
`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Ctx{
		Project:        "w3t",
		Slug:           "x",
		BaseDomain:     "w3t.test",
		TraefikNetwork: "pier",
		WorktreePath:   dir,
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    "docker-compose.yml",
			Service: "front",
		},
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080},
			{Service: "api", Port: 8000},
		},
		DefaultService: "front",
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)

	// Both exposed services get pier-managed container_name + ports reset
	for _, w := range []string{
		"container_name: w3t-x-api",
		"container_name: w3t-x-front",
		"traefik.http.routers.w3t-x-front-default.rule=Host(`x.w3t.test`)",
		"traefik.http.routers.w3t-x-api.rule=Host(`api.x.w3t.test`)",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}
	// Both exposed services have their host ports reset; redis isn't exposed
	// and has no ports/container_name in the user file → no entry needed.
	if strings.Count(s, "ports: !reset []") != 2 {
		t.Errorf("expected ports reset on both front and api, got:\n%s", s)
	}
	if strings.Contains(s, "  redis:\n") {
		t.Errorf("redis should not appear in override, got:\n%s", s)
	}
}

func TestRenderOverride_EnvInjection(t *testing.T) {
	c := Ctx{
		Project:        "w3t",
		Slug:           "x",
		BaseDomain:     "w3t.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    "docker-compose.yml",
			Service: "front",
		},
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080},
			{Service: "api", Port: 8000},
		},
		DefaultService: "front",
		Env: map[string]map[string]string{
			"front": {
				"API_URL":    "{url.api}",
				"PUBLIC_URL": "{url.default}",
			},
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)
	for _, w := range []string{
		"environment:",
		"- API_URL=http://api.x.w3t.test",
		"- PUBLIC_URL=http://x.w3t.test",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}
}

func TestRenderOverride_EnvOnNonExposedService(t *testing.T) {
	// Env injection on a service that's neither exposed nor mentioned in
	// the user's compose file at scan time should still produce a block —
	// otherwise the value would silently disappear.
	c := Ctx{
		Project:        "w3t",
		Slug:           "x",
		BaseDomain:     "w3t.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{{Service: "api", Port: 8000}},
		Env: map[string]map[string]string{
			"worker": {"API_URL": "{url.api}"},
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "  worker:\n    environment:\n      - API_URL=http://api.x.w3t.test") {
		t.Errorf("worker env missing or mis-shaped:\n%s", s)
	}
}

func TestRenderOverride_EnvBadToken(t *testing.T) {
	c := Ctx{
		Project:        "w3t",
		Slug:           "x",
		BaseDomain:     "w3t.test",
		TraefikNetwork: "pier",
		Stack:          manifest.Stack{Kind: manifest.KindCompose, File: "docker-compose.yml"},
		Expose:         []manifest.ExposeRule{{Service: "api", Port: 8000}},
		Env:            map[string]map[string]string{"api": {"X": "{url.ghost}"}},
	}
	_, err := renderOverride(c)
	if err == nil {
		t.Fatal("expected error on unknown service in env template")
	}
}

func TestURLs_AndDefault(t *testing.T) {
	c := Ctx{
		Slug:           "x",
		BaseDomain:     "w3t.test",
		Expose:         []manifest.ExposeRule{{Service: "front", Port: 8080}, {Service: "api", Port: 8000, Host: "backend"}},
		DefaultService: "front",
	}
	urls := URLs(c)
	want := []string{"http://x.w3t.test", "http://front.x.w3t.test", "http://backend.x.w3t.test"}
	if len(urls) != len(want) {
		t.Fatalf("URLs = %v, want %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("URLs[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
	if got := DefaultURL(c); got != "http://x.w3t.test" {
		t.Errorf("DefaultURL = %q", got)
	}

	// No default → DefaultURL falls back to first expose.
	c.DefaultService = ""
	if got := DefaultURL(c); got != "http://front.x.w3t.test" {
		t.Errorf("DefaultURL fallback = %q", got)
	}
	if got := URLs(c); len(got) != 2 {
		t.Errorf("URLs without default = %v, want 2 entries (no alias)", got)
	}
}

func TestFor(t *testing.T) {
	if a, err := For(manifest.KindCompose); err != nil || a == nil {
		t.Errorf("For(compose) = (%v, %v), want non-nil adapter", a, err)
	}
	if _, err := For("nonsense"); err == nil {
		t.Error("For(nonsense) should error")
	}
}

func TestNameAndService(t *testing.T) {
	if Name("myapp", "x") != "myapp-x" {
		t.Errorf("Name = %q", Name("myapp", "x"))
	}
	if ServiceName("myapp", "x", "api") != "myapp-x-api" {
		t.Errorf("ServiceName = %q", ServiceName("myapp", "x", "api"))
	}
}
