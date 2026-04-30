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
	got := resolveBinds("10.0.0.1", cfg, nil)
	if len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("explicit --bind should short-circuit, got %+v", got)
	}
}

func TestResolveBindsLocalIncludesLoopback(t *testing.T) {
	// docker may or may not be available in the test env; we only assert
	// loopback is always present and the result has no duplicates.
	cfg := &infra.Config{Mode: infra.ModeLocal}
	got := resolveBinds("", cfg, nil)
	if len(got) == 0 || got[0] != "127.0.0.1" {
		t.Errorf("loopback must be first, got %+v", got)
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a] {
			t.Errorf("duplicate bind %q in %+v", a, got)
		}
		seen[a] = true
	}
}

func TestResolveBindsServerRecordsAddsTailnet(t *testing.T) {
	cfg := &infra.Config{
		Mode:                 infra.ModeServer,
		HeadscaleRecordsPath: "/tmp/records.json",
		AnswerIP:             "100.64.0.10",
	}
	got := resolveBinds("", cfg, nil)
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

func TestResolveBindsServerWithoutRecordsKeepsLoopback(t *testing.T) {
	cfg := &infra.Config{Mode: infra.ModeServer, AnswerIP: "100.64.0.10"}
	got := resolveBinds("", cfg, nil)
	for _, a := range got {
		if a == "100.64.0.10" {
			t.Errorf("server-without-records should not advertise tailnet IP, got %+v", got)
		}
	}
}

func TestPrimaryReachableBind(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"127.0.0.1"}, "127.0.0.1"},                     // fallback
		{[]string{"127.0.0.1", "172.17.0.1"}, "172.17.0.1"},      // fallback last
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
