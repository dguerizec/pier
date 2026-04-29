package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/state"
)

func newTestAPI(t *testing.T) (*apiHandler, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	paths := &infra.Paths{
		Root:    dir,
		StateDB: filepath.Join(dir, "state.db"),
	}
	// Pre-create the state DB so List/Get don't blow up on a missing file.
	store, err := state.Open(paths.StateDB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	store.Close()

	cfg := &infra.Config{
		Mode:           infra.ModeLocal,
		TLD:            "test",
		BindIP:         "127.0.0.1",
		AnswerIP:       "127.0.0.1",
		TraefikNetwork: "pier",
	}
	h := &apiHandler{paths: paths, cfg: cfg}
	mux := http.NewServeMux()
	h.register(mux)
	return h, mux
}

func TestAPIConfigEndpoint(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var got apiConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TLD != "test" || got.Mode != infra.ModeLocal || got.BindIP != "127.0.0.1" {
		t.Errorf("unexpected config: %+v", got)
	}
	if got.TraefikNetwork != "pier" {
		t.Errorf("traefik_network = %q, want pier", got.TraefikNetwork)
	}
}

func TestAPIWorkloadsEmpty(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workloads", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	// Empty list must serialize as `[]`, not `null` — clients break on null.
	body := rec.Body.String()
	var got []apiWorkload
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty array, got %d items", len(got))
	}
	if body == "null\n" {
		t.Errorf("got `null`, want `[]`")
	}
}

func TestAPIWorkloadNotFound(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workloads/proj/slug", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == "" {
		t.Errorf("expected error field, got %v", body)
	}
}

func TestAPIUnknownRoute(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 on unknown route, got %d", rec.Code)
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// stdlib mux returns 405 when method doesn't match a registered pattern.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
