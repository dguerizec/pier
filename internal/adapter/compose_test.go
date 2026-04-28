package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/manifest"
)

func TestRenderOverride_Compose(t *testing.T) {
	c := Ctx{
		Project:        "myapp",
		Slug:           "feat-x",
		BaseDomain:     "myapp.test",
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    "docker-compose.yml",
			Service: "web",
			Port:    3000,
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)

	want := []string{
		"container_name: myapp-feat-x",
		"traefik.enable=true",
		"traefik.http.routers.myapp-feat-x.rule=Host(`feat-x.myapp.test`)",
		"traefik.http.routers.myapp-feat-x.entrypoints=web",
		"traefik.http.routers.myapp-feat-x.service=myapp-feat-x",
		"traefik.docker.network=pier",
		"traefik.http.services.myapp-feat-x.loadbalancer.server.port=3000",
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
			Port:         3000,
			MatchHostUID: true,
		},
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

	// Without the flag, no user line should appear.
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
			Port:    8080,
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(got)

	wantSubstrings := []string{
		// exposed service: pier-managed name, traefik labels, host ports stripped
		"container_name: w3t-x",
		"traefik.http.routers.w3t-x.rule=Host(`x.w3t.test`)",
		// other service that had ports + explicit container_name → both reset
		"api:\n    container_name: !reset null\n    ports: !reset []",
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}

	// front block must reset its host ports too (traefik routes via the pier
	// network, host ports would collide between worktrees).
	if !strings.Contains(s, "front:\n    container_name: w3t-x") {
		t.Errorf("expected front block to start with pier container_name\n%s", s)
	}
	if strings.Count(s, "ports: !reset []") != 2 {
		t.Errorf("expected ports reset on both front and api, got:\n%s", s)
	}

	// redis has no ports and no explicit container_name → no entry needed.
	if strings.Contains(s, "  redis:\n") {
		t.Errorf("redis should not appear in override, got:\n%s", s)
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

func TestNameAndURL(t *testing.T) {
	if Name("myapp", "x") != "myapp-x" {
		t.Errorf("Name = %q", Name("myapp", "x"))
	}
	if URL("x", "myapp.test") != "http://x.myapp.test" {
		t.Errorf("URL = %q", URL("x", "myapp.test"))
	}
}
