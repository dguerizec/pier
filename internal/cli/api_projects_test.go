package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/state"
)

func TestAPIScanProjectMissingRepo(t *testing.T) {
	_, mux := newTestAPI(t)
	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/scan",
		strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAPIScanProjectInvalidJSON(t *testing.T) {
	_, mux := newTestAPI(t)
	body := `not-json`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/scan",
		strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPIScanProjectUnscannablePath(t *testing.T) {
	// Pointing scan at a non-repo path returns 422 with a useful detail
	// — caller can show it to the user.
	_, mux := newTestAPI(t)
	body := `{"repo":"/nonexistent/path/that/cannot/be/a/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/scan",
		strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAPICreateProjectMissingFields(t *testing.T) {
	_, mux := newTestAPI(t)
	cases := []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"repo":"/x"}`, http.StatusBadRequest},
		{`{"manifest":{}}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects",
			strings.NewReader(tc.body))
		req.ContentLength = int64(len(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("body %q: got %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

func TestAPIListProjectsEmpty(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if body == "null\n" {
		t.Errorf("expected [], got null")
	}
	var got []apiProjectListItem
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d items", len(got))
	}
}

func TestAPIListProjectsReturnsRegistered(t *testing.T) {
	h, mux := newTestAPI(t)
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject("alpha", "/repos/alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject("bravo", "/repos/bravo"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []apiProjectListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "bravo" {
		t.Errorf("order = %+v", got)
	}
	for _, p := range got {
		if p.HasManifest {
			t.Errorf("project %s reported manifest at fake repo path", p.Name)
		}
	}
}

func TestAPIGetProjectNotFound(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAPIDeleteProjectIdempotent(t *testing.T) {
	// DELETE on a missing row is a no-op success — same shape as POST /down.
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/never-registered", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAPIListProjectWorktreesNotFound(t *testing.T) {
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nope/worktrees", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
