package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/infra"
)

func TestAutoBindServerRecordsMode(t *testing.T) {
	cfg := &infra.Config{
		Mode:                 infra.ModeServer,
		HeadscaleRecordsPath: "/tmp/records.json",
		AnswerIP:             "100.64.0.10",
		BindIP:               "0.0.0.0",
	}
	if got := autoBind(cfg); got != "100.64.0.10" {
		t.Errorf("autoBind server+records = %q, want 100.64.0.10", got)
	}
}

func TestAutoBindLocalMode(t *testing.T) {
	cfg := &infra.Config{
		Mode:     infra.ModeLocal,
		BindIP:   "127.0.0.1",
		AnswerIP: "127.0.0.1",
	}
	if got := autoBind(cfg); got != "127.0.0.1" {
		t.Errorf("autoBind local = %q, want 127.0.0.1", got)
	}
}

func TestAutoBindServerWithoutRecords(t *testing.T) {
	// Server install without records mode (dnsmasq path) — pier doesn't
	// know a single tailnet-reachable IP, so don't auto-expose. User can
	// pass --bind explicitly.
	cfg := &infra.Config{
		Mode:     infra.ModeServer,
		AnswerIP: "100.64.0.10",
	}
	if got := autoBind(cfg); got != "127.0.0.1" {
		t.Errorf("autoBind server-without-records = %q, want 127.0.0.1", got)
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
