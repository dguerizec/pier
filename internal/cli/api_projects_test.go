package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dguerizec/pier/internal/state"
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

func TestAPIListProjectWorktreesIncludesMissingWorkloads(t *testing.T) {
	h, mux := newTestAPI(t)
	repo := t.TempDir()
	if err := exec.Command("git", "init", repo).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject("alpha", repo); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(repo, ".pier", "worktrees", "gone")
	if err := store.Upsert(&state.Workload{
		Project:      "alpha",
		Slug:         "gone",
		Branch:       "feat/gone",
		Kind:         "compose",
		WorktreePath: missingPath,
		ContainerID:  "missing-container",
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/worktrees", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []apiWorktreeListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, wt := range got {
		if wt.Slug == "gone" {
			found = true
			if !wt.Missing || !wt.HasWorkload {
				t.Fatalf("missing workload flags = missing:%v has:%v", wt.Missing, wt.HasWorkload)
			}
			if wt.Path != missingPath {
				t.Fatalf("path = %q, want %q", wt.Path, missingPath)
			}
			if wt.Workload == nil || !wt.Workload.WorktreeMissing {
				t.Fatalf("workload missing flag = %+v", wt.Workload)
			}
		}
	}
	if !found {
		t.Fatalf("missing workload not returned: %+v", got)
	}
}

func TestAPIPostWorktreeUnknownProject(t *testing.T) {
	// Specifying a project that isn't registered must return 404, not
	// 500 — sillage / the UI need to distinguish "fix the request" from
	// "server is broken".
	_, mux := newTestAPI(t)
	body := `{"project":"unknown-proj","slug":"feat-x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/worktrees",
		strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAPIDeleteWorktreeProjectQuery(t *testing.T) {
	// DELETE accepts ?project=<name> as an alternative to ?repo=<path>.
	// Unknown project = 404 (same shape as POST).
	_, mux := newTestAPI(t)
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/worktrees/feat-x?project=unknown-proj", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAPIDeleteMissingWorktreeDropsOrphanState(t *testing.T) {
	h, mux := newTestAPI(t)
	repo := t.TempDir()
	if err := exec.Command("git", "init", repo).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterProject("alpha", repo); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(repo, ".pier", "worktrees", "gone")
	if err := store.Upsert(&state.Workload{
		Project:      "alpha",
		Slug:         "gone",
		Branch:       "feat/gone",
		Kind:         "compose",
		WorktreePath: missingPath,
		ContainerID:  "missing-container",
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/worktrees/gone?project=alpha", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got apiActionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "removed" || !strings.Contains(got.Warning, "state row dropped") {
		t.Fatalf("unexpected response: %+v", got)
	}

	store, err = state.Open(h.paths.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Get("alpha", "gone"); err != state.ErrNotFound {
		t.Fatalf("state row err = %v, want ErrNotFound", err)
	}
}
