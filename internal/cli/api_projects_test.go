package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeoPartt/pier/internal/state"
)

// projectFixture creates a temp project on disk (manifest + compose +
// overlay) and registers it in the state store. Returns the absolute
// project path so handlers resolve files correctly.
func projectFixture(t *testing.T, h *apiHandler, name string) string {
	t.Helper()
	dir := t.TempDir()

	manifest := []byte(`[project]
  name = "` + name + `"
  base_domain = "` + name + `.test"

[stack]
  kind = "compose"
  file = "docker-compose.dev.yml"
  service = "web"

[[expose]]
  service = "web"
  port = 5173
`)
	if err := os.WriteFile(filepath.Join(dir, ".pier.toml"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.dev.yml"),
		[]byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".pier"), 0o755); err != nil {
		t.Fatalf("mkdir .pier: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pier", "compose.override.yml"),
		[]byte("# pier-managed\nservices: {}\n"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	if err := store.UpsertProject(&state.Project{
		Name:         name,
		Path:         dir,
		BaseDomain:   name + ".test",
		StackFile:    "docker-compose.dev.yml",
		StackService: "web",
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	return dir
}

func TestAPIProjects_List(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")
	projectFixture(t, h, "beta")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	var got []apiProject
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("expected [alpha beta], got %+v", got)
	}
}

func TestAPIProjects_GetAndDelete(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d body %s", rec.Code, rec.Body)
	}
	var p apiProject
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Name != "alpha" || p.BaseDomain != "alpha.test" {
		t.Fatalf("unexpected payload: %+v", p)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/projects/alpha", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get-after-delete: status %d body %s", rec.Code, rec.Body)
	}
}

func TestAPIProjects_GetMissing(t *testing.T) {
	_, mux := newTestAPI(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/none", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestAPIProjects_InvalidName(t *testing.T) {
	_, mux := newTestAPI(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/..%2Fetc/manifest", nil))
	// {name} captures a single segment; ".." would even pass the SQL
	// lookup (fail with not-found), but we want the regex to reject it.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/Bad_Name", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestAPIProjects_GetManifest(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/manifest", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/toml" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `name = "alpha"`) {
		t.Errorf("body missing project name:\n%s", rec.Body)
	}
}

func TestAPIProjects_PutManifest_Valid(t *testing.T) {
	h, mux := newTestAPI(t)
	dir := projectFixture(t, h, "alpha")

	body := []byte(`# my comment
[project]
  name = "alpha"
  base_domain = "alpha.elsewhere.test"

[stack]
  kind = "compose"
  file = "docker-compose.dev.yml"
  service = "web"

[[expose]]
  service = "web"
  port = 5173
`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/projects/alpha/manifest", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".pier.toml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("manifest bytes mismatch — comments / formatting should be preserved\n--- got ---\n%s", got)
	}
	// Registry should refresh base_domain.
	store, _ := state.Open(h.paths.StateDB)
	defer store.Close()
	p, _ := store.GetProject("alpha")
	if p.BaseDomain != "alpha.elsewhere.test" {
		t.Errorf("registry base_domain = %q, want refreshed value", p.BaseDomain)
	}
}

func TestAPIProjects_PutManifest_InvalidTOML(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/projects/alpha/manifest",
		strings.NewReader("this is not toml [[")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestAPIProjects_PutManifest_RejectRename(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	body := []byte(`[project]
  name = "renamed"

[stack]
  kind = "compose"
  file = "docker-compose.dev.yml"

[[expose]]
  service = "web"
  port = 5173
`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/projects/alpha/manifest", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestAPIProjects_GetCompose(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/compose", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "image: nginx") {
		t.Errorf("body missing compose contents:\n%s", rec.Body)
	}
}

func TestAPIProjects_GetOverlay(t *testing.T) {
	h, mux := newTestAPI(t)
	projectFixture(t, h, "alpha")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/overlay", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pier-managed") {
		t.Errorf("body missing overlay marker:\n%s", rec.Body)
	}
}

func TestAPIProjects_GetOverlay_NeverUp(t *testing.T) {
	h, mux := newTestAPI(t)
	dir := projectFixture(t, h, "alpha")
	if err := os.RemoveAll(filepath.Join(dir, ".pier")); err != nil {
		t.Fatalf("rm overlay: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/alpha/overlay", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}
