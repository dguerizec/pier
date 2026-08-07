package adapter

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dguerizec/pier/internal/manifest"
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
		"networks:\n      pier_proxy:",
		"- web.feat-x.myapp.test",
		"- feat-x.myapp.test",
		"name: pier",
		"external: true",
	}
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("override missing %q\n--- rendered ---\n%s", w, s)
		}
	}
	// Compose connects the proxy network before Traefik discovers the
	// container. AttachToTraefikNetwork then reconnects it with only these
	// FQDN aliases, removing Compose's implicit short service alias.
}

func TestComposePrepareApplyBuildsBeforeReconcileAndWaits(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("shell-script docker stub is POSIX-only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services:\n  web:\n    image: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CALLS_FILE"
case "$*" in
  *"config --format=json"*)
    printf '%s\n' '{"services":{"web":{}},"networks":{"default":{"name":"demo-main_default","external":false},"pier_proxy":{"name":"pier","external":true}}}'
    ;;
  "network ls"*) printf '%s\n' 'pier' ;;
  *"ps -q web"*) printf '%s\n' 'container-id' ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS_FILE", calls)
	c := Ctx{
		Project:        "demo",
		Slug:           "main",
		BaseDomain:     "demo.test",
		WorktreePath:   dir,
		Stack:          manifest.Stack{Kind: manifest.KindCompose, File: "compose.yml", Service: "web"},
		Expose:         []manifest.ExposeRule{{Service: "web", Port: 3000}},
		DefaultService: "web",
		TraefikNetwork: "pier",
		WaitTimeout:    7 * time.Second,
		Out:            io.Discard,
		Err:            io.Discard,
	}
	a := compose{}
	prepared, err := a.Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prepared.AdapterData), "demo-main_default") ||
		!strings.Contains(string(prepared.AdapterData), teardownImage) {
		t.Fatalf("teardown config missing applied resources:\n%s", prepared.AdapterData)
	}
	if _, err := a.Apply(c, prepared); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	log := string(body)
	build := strings.Index(log, " build\n")
	up := strings.Index(log, " up -d --no-build --remove-orphans --wait --wait-timeout 7\n")
	if build < 0 || up < 0 || build > up {
		t.Fatalf("build must precede reconcile:\n%s", log)
	}
	if strings.Contains(log, " restart ") {
		t.Fatalf("normal apply must not restart exposed containers:\n%s", log)
	}
	if !strings.Contains(log, "network disconnect pier demo-main-web") ||
		!strings.Contains(log, "network connect --alias web.main.demo.test --alias main.demo.test pier demo-main-web") {
		t.Fatalf("exact Traefik alias reconnect missing:\n%s", log)
	}
}

func TestComposeDownAppliedUsesFrozenTeardownConfig(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("shell-script docker stub is POSIX-only")
	}
	dir := t.TempDir()
	stubDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS_FILE\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS_FILE", calls)
	c := Ctx{Project: "old", Slug: "main", WorktreePath: dir, Out: io.Discard, Err: io.Discard}
	data := []byte("services:\n  web:\n    image: pier.local/teardown-placeholder\n")
	if err := (compose{}).DownApplied(c, data); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	log := string(body)
	if !strings.Contains(log, "compose -f ") || !strings.Contains(log, " -p old-main down --remove-orphans") {
		t.Fatalf("applied down invocation = %q", log)
	}
	if strings.Contains(log, "compose.yml") {
		t.Fatalf("applied down unexpectedly used current stack file: %q", log)
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

func TestRenderOverride_AvoidsSourceNetworkKeyCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(`services:
  web:
    image: demo
networks:
  pier_proxy:
    name: user-owned
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Ctx{
		Project: "demo", Slug: "main", BaseDomain: "demo.test", WorktreePath: dir,
		Stack:  manifest.Stack{Kind: manifest.KindCompose, File: "compose.yml"},
		Expose: []manifest.ExposeRule{{Service: "web", Port: 3000}}, TraefikNetwork: "pier",
	}
	body, err := renderOverride(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "pier_proxy_1:") {
		t.Fatalf("generated network key should avoid source collision:\n%s", body)
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

	c.Service = map[string]manifest.ServiceConfig{
		"web": {MatchHostUID: true},
	}
	got, err = renderOverride(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), expected) {
		t.Errorf("per-service match_host_uid should inject %q, got:\n%s", expected, got)
	}
}

func TestRenderOverride_MatchHostUIDNonExposedService(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  front:
    image: node:20-alpine
  worker:
    image: python:3.12-slim
`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Ctx{
		Project:        "myapp",
		Slug:           "x",
		BaseDomain:     "myapp.test",
		WorktreePath:   dir,
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{{Service: "front", Port: 3000}},
		Service: map[string]manifest.ServiceConfig{
			"worker": {MatchHostUID: true},
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	expected := fmt.Sprintf(`user: "%d:%d"`, os.Getuid(), os.Getgid())
	if !strings.Contains(s, "  worker:\n    "+expected) {
		t.Errorf("non-exposed worker should get user override, got:\n%s", s)
	}
	if strings.Contains(sectionForService(s, "worker"), "traefik.enable=true") {
		t.Errorf("non-exposed worker should not get traefik labels, got:\n%s", s)
	}
}

func TestRenderOverride_MatchHostUIDUnknownService(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  front:
    image: node:20-alpine
`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Ctx{
		Project:        "myapp",
		Slug:           "x",
		BaseDomain:     "myapp.test",
		WorktreePath:   dir,
		TraefikNetwork: "pier",
		Stack: manifest.Stack{
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{{Service: "front", Port: 3000}},
		Service: map[string]manifest.ServiceConfig{
			"ghost": {MatchHostUID: true},
		},
	}
	if _, err := renderOverride(c); err == nil {
		t.Fatal("renderOverride should reject service.match_host_uid for unknown compose service")
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

func TestRenderOverride_PreserveSelectedHostBindings(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  front:
    image: node:20-alpine
    ports:
      - "60180:8080"
      - "2223:2223"
      - target: 2224
        published: 2224
        protocol: tcp
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
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080, PreservePorts: []int{2223, 2224}},
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	front := sectionForService(string(got), "front")
	for _, want := range []string{
		"ports: !override",
		`"2223:2223"`,
		"target: 2224",
		"published: 2224",
	} {
		if !strings.Contains(front, want) {
			t.Errorf("front override missing %q:\n%s", want, front)
		}
	}
	for _, bad := range []string{
		"ports: !reset []",
		"60180:8080",
	} {
		if strings.Contains(front, bad) {
			t.Errorf("front override should not contain %q:\n%s", bad, front)
		}
	}
}

func TestRenderOverride_PreserveResolvedValueHostBinding(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  backend:
    image: python:3.13-slim
    ports:
      - "127.0.0.1:${PICKATUBE_OAUTH_RELAY_PORT:-8765}:8765"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Ctx{
		Project:        "pickatube",
		Slug:           "oauth",
		BaseDomain:     "pickatube.test",
		TraefikNetwork: "pier",
		WorktreePath:   dir,
		Stack: manifest.Stack{
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{
			{Service: "backend", Port: 8000, PreservePorts: []int{49163}},
		},
		ComposeEnv: map[string]string{
			"PICKATUBE_OAUTH_RELAY_PORT": "49163",
		},
	}
	got, err := renderOverride(c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	backend := sectionForService(string(got), "backend")
	if !strings.Contains(backend, `"127.0.0.1:49163:8765"`) {
		t.Errorf("resolved host binding missing:\n%s", backend)
	}
	if strings.Contains(backend, "PICKATUBE_OAUTH_RELAY_PORT") {
		t.Errorf("unresolved value reference remains:\n%s", backend)
	}
}

func TestComposeRun_PropagatesResolvedValueEnvironment(t *testing.T) {
	dir := t.TempDir()
	stubDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "captured")
	script := "#!/bin/sh\nprintf '%s|%s' \"$PIER_VALUE_OAUTH_CALLBACK_PORT\" \"$PICKATUBE_OAUTH_RELAY_PORT\" > \"$CAPTURE_FILE\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_FILE", capture)

	c := Ctx{
		Project:      "pickatube",
		Slug:         "oauth",
		WorktreePath: dir,
		Stack: manifest.Stack{
			File: "docker-compose.yml",
		},
		ComposeEnv: map[string]string{
			"PIER_VALUE_OAUTH_CALLBACK_PORT": "49163",
			"PICKATUBE_OAUTH_RELAY_PORT":     "49163",
		},
	}
	if _, err := composeRun(c, []string{"config"}, filepath.Join(dir, "override.yml"), true); err != nil {
		t.Fatalf("composeRun: %v", err)
	}
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "49163|49163" {
		t.Errorf("captured value = %q, want 49163|49163", got)
	}
}

func TestExpandComposeEnv(t *testing.T) {
	env := map[string]string{
		"PORT":  "49163",
		"EMPTY": "",
		"ALT":   "fallback",
	}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"braced", "${PORT}:8765", "49163:8765"},
		{"bare", "$PORT:8765", "49163:8765"},
		{"default", "${PORT:-8765}:8765", "49163:8765"},
		{"empty default", "${EMPTY:-8765}:8765", "8765:8765"},
		{"alternate", "${PORT:+$ALT}:8765", "fallback:8765"},
		{"unknown", "${FROM_DOTENV:-8765}:8765", "${FROM_DOTENV:-8765}:8765"},
		{"escaped", "$${PORT}:8765", "$${PORT}:8765"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := expandComposeEnv(c.input, env); got != c.want {
				t.Errorf("expandComposeEnv(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestRenderOverride_PreserveHostBindingMissingPort(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(stack, []byte(`services:
  front:
    image: node:20-alpine
    ports:
      - "2223:2223"
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
			Kind: manifest.KindCompose,
			File: "docker-compose.yml",
		},
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080, PreservePorts: []int{2224}},
		},
	}
	if _, err := renderOverride(c); err == nil {
		t.Fatal("renderOverride should reject preserve_ports when the compose binding is missing")
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

func sectionForService(s, service string) string {
	start := strings.Index(s, "\n  "+service+":\n")
	if start == -1 {
		return ""
	}
	rest := s[start+1:]
	offset := len("  " + service + ":\n")
	for {
		next := strings.Index(rest[offset:], "\n  ")
		if next == -1 {
			return rest
		}
		next += offset
		if next+3 < len(rest) && rest[next+3] != ' ' {
			return rest[:next]
		}
		offset = next + 1
	}
}
