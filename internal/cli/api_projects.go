package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
)

// projectNameRE constrains path-segment values used to look up rows in
// the projects table. Even though the SQL layer is parameterised, an
// untrusted name eventually flows into filesystem paths (via the row's
// Path column) — and the row was inserted by `pier init`, which already
// validates the name as a DNS label. Reject anything else here so an
// attacker can't smuggle e.g. "../" into a future file-serving handler.
var projectNameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

type apiProject struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	BaseDomain   string    `json:"base_domain"`
	StackFile    string    `json:"stack_file"`
	StackService string    `json:"stack_service,omitempty"`
	LastSeen     time.Time `json:"last_seen"`
	Workloads    int       `json:"workloads"`
}

func (h *apiHandler) listProjects(w http.ResponseWriter, _ *http.Request) {
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	projects, err := store.ListProjects()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	wls, err := store.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts := make(map[string]int, len(projects))
	for _, wl := range wls {
		counts[wl.Project]++
	}

	out := make([]apiProject, 0, len(projects))
	for _, p := range projects {
		out = append(out, toAPIProject(p, counts[p.Name]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) getProject(w http.ResponseWriter, r *http.Request) {
	name, ok := projectNameFromPath(w, r)
	if !ok {
		return
	}
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	p, err := store.GetProject(name)
	if err != nil {
		respondProjectErr(w, name, err)
		return
	}
	wls, err := store.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	count := 0
	for _, wl := range wls {
		if wl.Project == name {
			count++
		}
	}
	writeJSON(w, http.StatusOK, toAPIProject(p, count))
}

func (h *apiHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	name, ok := projectNameFromPath(w, r)
	if !ok {
		return
	}
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	if _, err := store.GetProject(name); err != nil {
		respondProjectErr(w, name, err)
		return
	}
	if err := store.DeleteProject(name); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getProjectManifest returns the raw .pier.toml bytes. Comments and
// formatting are preserved — the file on disk is the source of truth.
func (h *apiHandler) getProjectManifest(w http.ResponseWriter, r *http.Request) {
	p, ok := h.lookupProject(w, r)
	if !ok {
		return
	}
	serveFileBytes(w, filepath.Join(p.Path, manifest.FileName), "application/toml")
}

// putProjectManifest replaces .pier.toml with the request body after
// parsing + Validate(). The raw bytes are written back so user
// formatting / comments survive the round-trip; we only re-encode if
// the body is invalid TOML (in which case we reject 400 anyway).
func (h *apiHandler) putProjectManifest(w http.ResponseWriter, r *http.Request) {
	p, ok := h.lookupProject(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var m manifest.Manifest
	if _, err := toml.Decode(string(body), &m); err != nil {
		writeAPIError(w, http.StatusBadRequest, "parse manifest: "+err.Error())
		return
	}
	if err := m.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validate manifest: "+err.Error())
		return
	}
	// Refuse renames via PUT — too many side effects (state DB rows,
	// running containers, traefik routes). Force the user to re-init.
	if m.Project.Name != p.Name {
		writeAPIError(w, http.StatusBadRequest,
			fmt.Sprintf("project.name in body (%q) must match URL (%q)", m.Project.Name, p.Name))
		return
	}
	target := filepath.Join(p.Path, manifest.FileName)
	if err := atomicWriteFile(target, body, 0o644); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Refresh the registry — base_domain / stack file / service may
	// have moved.
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()
	_ = store.UpsertProject(&state.Project{
		Name:         m.Project.Name,
		Path:         p.Path,
		BaseDomain:   m.Project.BaseDomain,
		StackFile:    m.Stack.File,
		StackService: m.Stack.Service,
		LastSeen:     time.Now(),
	})

	w.WriteHeader(http.StatusNoContent)
}

// getProjectCompose serves the compose file referenced by the manifest,
// read-only. Pier never mutates the user's compose; estibador-style
// editors must round-trip through the user's editor + git.
func (h *apiHandler) getProjectCompose(w http.ResponseWriter, r *http.Request) {
	p, ok := h.lookupProject(w, r)
	if !ok {
		return
	}
	composePath := p.StackFile
	if !filepath.IsAbs(composePath) {
		composePath = filepath.Join(p.Path, composePath)
	}
	serveFileBytes(w, composePath, "application/yaml")
}

// getProjectOverlay serves .pier/compose.override.yml — pier-generated
// at every `pier up`. Read-only by design: edits would be clobbered on
// the next up. 404 if the project has never been brought up.
func (h *apiHandler) getProjectOverlay(w http.ResponseWriter, r *http.Request) {
	p, ok := h.lookupProject(w, r)
	if !ok {
		return
	}
	serveFileBytes(w, filepath.Join(p.Path, ".pier", "compose.override.yml"), "application/yaml")
}

// projectNameFromPath validates {name} from the URL and writes a 400
// when it doesn't match the allowed shape.
func projectNameFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "missing project name")
		return "", false
	}
	if !projectNameRE.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "invalid project name")
		return "", false
	}
	return name, true
}

// lookupProject combines name validation, store open and lookup. Used
// by handlers that need the full Project row.
func (h *apiHandler) lookupProject(w http.ResponseWriter, r *http.Request) (*state.Project, bool) {
	name, ok := projectNameFromPath(w, r)
	if !ok {
		return nil, false
	}
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	defer store.Close()

	p, err := store.GetProject(name)
	if err != nil {
		respondProjectErr(w, name, err)
		return nil, false
	}
	return p, true
}

func respondProjectErr(w http.ResponseWriter, name string, err error) {
	if errors.Is(err, state.ErrProjectNotFound) {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("project %q not found", name))
		return
	}
	writeAPIError(w, http.StatusInternalServerError, err.Error())
}

func toAPIProject(p *state.Project, workloads int) apiProject {
	return apiProject{
		Name:         p.Name,
		Path:         p.Path,
		BaseDomain:   p.BaseDomain,
		StackFile:    p.StackFile,
		StackService: p.StackService,
		LastSeen:     p.LastSeen,
		Workloads:    workloads,
	}
}

// serveFileBytes streams a file's bytes with the given content type.
// 404 on missing, 500 on read errors. Used for raw manifest / compose
// / overlay endpoints.
func serveFileBytes(w http.ResponseWriter, path, contentType string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("file not found: %s", path))
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

// atomicWriteFile writes data to a sibling .tmp then renames. Avoids
// half-written manifests if the process dies mid-write — the same
// concern traefik_route.go has for the dynamic config file.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
