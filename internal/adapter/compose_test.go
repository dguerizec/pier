package adapter

import (
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/manifest"
)

func TestRenderOverride_Compose(t *testing.T) {
	c := Ctx{
		Project:    "myapp",
		Slug:       "feat-x",
		BaseDomain: "myapp.test",
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

func TestFor(t *testing.T) {
	if a, err := For(manifest.KindCompose); err != nil || a == nil {
		t.Errorf("For(compose) = (%v, %v), want non-nil adapter", a, err)
	}
	if _, err := For(manifest.KindProcess); err == nil {
		t.Error("For(process) should be unsupported in MVP")
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
