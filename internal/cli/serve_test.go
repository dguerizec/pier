package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/infra"
)

func TestResolveBindsExplicit(t *testing.T) {
	cfg := &infra.Config{Mode: infra.ModeLocal}
	got := resolveBinds("10.0.0.1", cfg, "10.10.6.1")
	if len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("explicit --bind should short-circuit (ignore bridge), got %+v", got)
	}
}

func TestResolveBindsLocalIncludesLoopback(t *testing.T) {
	cfg := &infra.Config{Mode: infra.ModeLocal}
	got := resolveBinds("", cfg, "")
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("loopback-only when bridge absent, got %+v", got)
	}
}

func TestResolveBindsAppendsBridgeGateway(t *testing.T) {
	cfg := &infra.Config{Mode: infra.ModeLocal}
	got := resolveBinds("", cfg, "10.10.6.1")
	if len(got) != 2 || got[0] != "127.0.0.1" || got[1] != "10.10.6.1" {
		t.Errorf("expected [127.0.0.1 10.10.6.1], got %+v", got)
	}
}

func TestResolveBindsDeduplicates(t *testing.T) {
	cfg := &infra.Config{
		Mode:                 infra.ModeServer,
		HeadscaleRecordsPath: "/tmp/records.json",
		AnswerIP:             "10.10.6.1", // coincides with bridge
	}
	got := resolveBinds("", cfg, "10.10.6.1")
	seen := map[string]int{}
	for _, a := range got {
		seen[a]++
	}
	for a, n := range seen {
		if n > 1 {
			t.Errorf("duplicate bind %q (%d) in %+v", a, n, got)
		}
	}
}

func TestResolveBindsServerRecordsAddsTailnet(t *testing.T) {
	cfg := &infra.Config{
		Mode:                 infra.ModeServer,
		HeadscaleRecordsPath: "/tmp/records.json",
		AnswerIP:             "100.64.0.10",
	}
	got := resolveBinds("", cfg, "")
	found := false
	for _, a := range got {
		if a == "100.64.0.10" {
			found = true
		}
	}
	if !found {
		t.Errorf("tailnet IP not in binds: %+v", got)
	}
}

func TestResolveBindsServerSplitDNSAddsAnswerIP(t *testing.T) {
	// Split-DNS server install (no records mode) — AnswerIP is the
	// externally-reachable address peers will dial for pier.<tld>, so
	// pier serve must bind there for the dashboard route to land.
	cfg := &infra.Config{Mode: infra.ModeServer, AnswerIP: "100.64.0.10"}
	got := resolveBinds("", cfg, "")
	found := false
	for _, a := range got {
		if a == "100.64.0.10" {
			found = true
		}
	}
	if !found {
		t.Errorf("AnswerIP missing from binds for server+split-DNS install: %+v", got)
	}
}

func TestResolveBindsServerSkipsWildcard(t *testing.T) {
	// AnswerIP=0.0.0.0 is degenerate: pier serve already gets the
	// wildcard via 127.0.0.1's listener semantics. Adding 0.0.0.0
	// would double-bind and surface as a routing target we can't use.
	cfg := &infra.Config{Mode: infra.ModeServer, AnswerIP: "0.0.0.0"}
	got := resolveBinds("", cfg, "")
	for _, a := range got {
		if a == "0.0.0.0" {
			t.Errorf("0.0.0.0 should not appear in binds: %+v", got)
		}
	}
}

func TestResolveBindsLocalKeepsLoopback(t *testing.T) {
	// Local mode never binds AnswerIP — the user explicitly chose the
	// laptop-only path.
	cfg := &infra.Config{Mode: infra.ModeLocal, AnswerIP: "127.0.0.1"}
	got := resolveBinds("", cfg, "")
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("local mode should bind 127.0.0.1 only, got %+v", got)
	}
}

func TestDashboardRouteDir(t *testing.T) {
	paths := &infra.Paths{TraefikDynamic: "/var/pier/dynamic"}

	if got := dashboardRouteDir(&infra.Config{}, paths); got != "/var/pier/dynamic" {
		t.Errorf("pier-managed: got %q", got)
	}
	cfg := &infra.Config{ExternalTraefikDynamicDir: "/etc/traefik/dynamic"}
	if got := dashboardRouteDir(cfg, paths); got != "/etc/traefik/dynamic" {
		t.Errorf("BYO override: got %q", got)
	}
}

func TestDashboardUpstreamIP_PierManaged(t *testing.T) {
	cfg := &infra.Config{}

	// Bridge in binds → use it.
	if got := dashboardUpstreamIP(cfg, "10.10.6.1", []string{"127.0.0.1", "10.10.6.1"}); got != "10.10.6.1" {
		t.Errorf("got %q, want bridge", got)
	}
	// Bridge not in binds (e.g. --bind 127.0.0.1) → skip.
	if got := dashboardUpstreamIP(cfg, "10.10.6.1", []string{"127.0.0.1"}); got != "" {
		t.Errorf("got %q, want empty (bridge unreachable)", got)
	}
	// No bridge at all → skip.
	if got := dashboardUpstreamIP(cfg, "", []string{"127.0.0.1"}); got != "" {
		t.Errorf("got %q, want empty (no bridge)", got)
	}
}

func TestDashboardUpstreamIP_BYO(t *testing.T) {
	cfg := &infra.Config{ExternalTraefikDynamicDir: "/etc/traefik/dynamic"}

	// Tailnet IP in binds → preferred over bridge.
	got := dashboardUpstreamIP(cfg, "10.10.6.1", []string{"127.0.0.1", "10.10.6.1", "100.64.0.10"})
	if got != "100.64.0.10" {
		t.Errorf("BYO got %q, want tailnet", got)
	}
	// No peer-reachable IP but bridge present → fall back to bridge.
	got = dashboardUpstreamIP(cfg, "10.10.6.1", []string{"127.0.0.1", "10.10.6.1"})
	if got != "10.10.6.1" {
		t.Errorf("BYO fallback got %q, want bridge", got)
	}
	// Loopback only → nothing reachable.
	if got := dashboardUpstreamIP(cfg, "", []string{"127.0.0.1"}); got != "" {
		t.Errorf("BYO loopback-only got %q, want empty", got)
	}
}

func TestPrimaryReachableBind(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"127.0.0.1"}, ""},
		{[]string{"127.0.0.1", "172.17.0.1"}, ""},
		{[]string{"127.0.0.1", "10.10.6.1", "192.168.1.5"}, ""},
		{[]string{"127.0.0.1", "172.17.0.1", "100.64.0.10"}, "100.64.0.10"},
		{[]string{"127.0.0.1", "100.64.0.10"}, "100.64.0.10"},
	}
	for _, c := range cases {
		if got := primaryReachableBind(c.in); got != c.want {
			t.Errorf("primaryReachableBind(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCORSWildcard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withCORS(mux, []string{"*"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set("Origin", "https://sillage.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
}

func TestCORSAllowlist(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withCORS(mux, []string{"https://allowed.example", "https://other.example"})

	// Allowed origin echoes back.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set("Origin", "https://allowed.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Errorf("allowed origin: ACAO = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}

	// Disallowed origin gets no ACAO header — browser blocks.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin: ACAO = %q, want empty", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	mux := http.NewServeMux()
	// No GET handler — verify preflight short-circuits before hitting mux.
	h := withCORS(mux, []string{"*"})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/workloads", nil)
	req.Header.Set("Origin", "https://sillage.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Allow-Methods = %q, missing POST", got)
	}
}

func TestCORSDashboardUntouched(t *testing.T) {
	// Dashboard is same-origin; CORS headers shouldn't be added there.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("dashboard"))
	})
	h := withCORS(mux, []string{"*"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://anywhere.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("dashboard should not get CORS headers, ACAO = %q", got)
	}
}
