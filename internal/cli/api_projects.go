package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/LeoPartt/pier/internal/initwizard"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
	"github.com/LeoPartt/pier/internal/worktree"
)

// JSON shapes for /api/v1/projects/*. Same contract rules as api.go:
// add freely, never remove or rename without bumping to /v2.

type apiProject struct {
	Name             string             `json:"name"`
	RepoPath         string             `json:"repo_path"`
	RegisteredAt     string             `json:"registered_at"`
	HasManifest      bool               `json:"has_manifest"`
	Manifest         *manifest.Manifest `json:"manifest,omitempty"`
	ActiveWorkloads  int                `json:"active_workloads"`
	WorktreeCount    int                `json:"worktree_count"`
}

type apiProjectListItem struct {
	Name            string `json:"name"`
	RepoPath        string `json:"repo_path"`
	RegisteredAt    string `json:"registered_at"`
	HasManifest     bool   `json:"has_manifest"`
	ActiveWorkloads int    `json:"active_workloads"`
}

// apiScanRequest is the body of POST /api/v1/projects/scan.
type apiScanRequest struct {
	Repo string `json:"repo"`
}

type apiScanService struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Port  int    `json:"port"` // first published container port
}

// apiScanResponse describes what a fresh `pier init` would propose for
// the given repo. Pure read — no files are written.
type apiScanResponse struct {
	Repo             string             `json:"repo"`
	Toplevel         string             `json:"toplevel"`
	ManifestPath     string             `json:"manifest_path"`
	ComposeFile      string             `json:"compose_file,omitempty"`
	Services         []apiScanService   `json:"services"`
	ExistingManifest *manifest.Manifest `json:"existing_manifest,omitempty"`
	SuggestedManifest *manifest.Manifest `json:"suggested_manifest,omitempty"`
	IsReinit         bool               `json:"is_reinit"`
}

// apiProjectCreateRequest is the body of POST /api/v1/projects. The
// caller passes the FULL manifest verbatim — typically built by editing
// the suggested_manifest from a prior /scan. Server writes <repo>/.pier.toml
// and registers the (name, repo_path) pair in the state DB.
//
// `private` mirrors the CLI's --private flag: when true, .pier.toml is
// added to .gitignore on first init (the manifest stays per-machine).
// Default false matches the CLI default (manifest is committed so other
// contributors and secondary worktrees inherit it).
type apiProjectCreateRequest struct {
	Repo     string             `json:"repo"`
	Manifest *manifest.Manifest `json:"manifest"`
	Private  bool               `json:"private,omitempty"`
}

type apiProjectCreateResponse struct {
	Repo         string `json:"repo"`
	ProjectName  string `json:"project_name"`
	ManifestPath string `json:"manifest_path"`
	Registered   bool   `json:"registered"` // false only when registry rejected (conflict on different mapping)
	Merged       bool   `json:"merged"`     // true when a .pier.toml already existed
	Warning      string `json:"warning,omitempty"`
}

type apiWorktreeListItem struct {
	Path     string `json:"path"`
	Slug     string `json:"slug"` // basename of path
	Branch   string `json:"branch"`
	HasWorkload bool `json:"has_workload"`
	Workload *apiWorkload `json:"workload,omitempty"`
}

func (h *apiHandler) registerProjects(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/projects/scan", h.scanProject)
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("POST /api/v1/projects", h.createProject)
	mux.HandleFunc("GET /api/v1/projects/{name}", h.getProject)
	mux.HandleFunc("DELETE /api/v1/projects/{name}", h.deleteProject)
	mux.HandleFunc("GET /api/v1/projects/{name}/worktrees", h.listProjectWorktrees)
}

func (h *apiHandler) scanProject(w http.ResponseWriter, r *http.Request) {
	var body apiScanRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Repo == "" {
		writeAPIError(w, http.StatusBadRequest, "repo is required")
		return
	}
	abs, err := filepath.Abs(body.Repo)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "repo: "+err.Error())
		return
	}

	plan, _, derr := initwizard.Derive(abs, initwizard.Opts{Yes: true})
	if derr != nil {
		// Surface as 422 — caller passed a path that doesn't look like a
		// pier-able project (no compose file, missing repo, etc.).
		writeAPIError(w, http.StatusUnprocessableEntity, derr.Error())
		return
	}

	resp := apiScanResponse{
		Repo:         abs,
		Toplevel:     plan.Toplevel,
		ManifestPath: plan.ManifestPath,
		ComposeFile:  plan.ComposeFile,
		IsReinit:     plan.IsReinit(),
	}
	for _, c := range plan.Candidates {
		resp.Services = append(resp.Services, apiScanService{
			Name: c.Service,
			Port: c.Port,
		})
	}
	if plan.IsReinit() {
		resp.ExistingManifest = plan.Existing
	}
	if m, err := initwizard.ProposeManifest(plan); err == nil {
		resp.SuggestedManifest = m
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *apiHandler) createProject(w http.ResponseWriter, r *http.Request) {
	var body apiProjectCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Repo == "" {
		writeAPIError(w, http.StatusBadRequest, "repo is required")
		return
	}
	if body.Manifest == nil {
		writeAPIError(w, http.StatusBadRequest, "manifest is required")
		return
	}
	if err := body.Manifest.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "manifest: "+err.Error())
		return
	}
	abs, err := filepath.Abs(body.Repo)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "repo: "+err.Error())
		return
	}
	manifestPath := filepath.Join(abs, manifest.FileName)

	merged := false
	if _, err := manifest.Load(abs); err == nil {
		merged = true
	}
	if err := body.Manifest.Write(manifestPath); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "write manifest: "+err.Error())
		return
	}

	// Gitignore parity with the CLI wizard. Only on first init — re-init
	// must not second-guess the user's prior commit/share decisions.
	//
	// Always-private entries (per-machine state, secrets) are added every
	// time. .pier.toml is conditional on `private`: default share = commit
	// the manifest so secondary worktrees and other contributors inherit
	// the project's pier config. Failures are non-fatal — they accumulate
	// into a warning surfaced on the response.
	var ignoreWarn string
	if !merged {
		entries := []string{manifest.LocalFileName, ".pier/"}
		if body.Private {
			entries = append([]string{manifest.FileName}, entries...)
		}
		if e := initwizard.WorktreeDirGitignoreEntry(abs, body.Manifest.Worktree.Dir); e != "" {
			entries = append(entries, e)
		}
		for _, entry := range entries {
			if err := initwizard.EnsureGitignore(abs, entry); err != nil {
				if ignoreWarn != "" {
					ignoreWarn += "; "
				}
				ignoreWarn += "gitignore " + entry + ": " + err.Error()
			}
		}
	}

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	resp := apiProjectCreateResponse{
		Repo:         abs,
		ProjectName:  body.Manifest.Project.Name,
		ManifestPath: manifestPath,
		Registered:   true,
		Merged:       merged,
	}
	if _, err := store.RegisterProject(body.Manifest.Project.Name, abs); err != nil {
		if errors.Is(err, state.ErrProjectExists) {
			// Manifest is on disk; only the registry refused. Surface as
			// 200 with a warning so the caller can decide (most likely
			// they'll DELETE the conflicting row and retry).
			resp.Registered = false
			resp.Warning = err.Error()
		} else {
			writeAPIError(w, http.StatusInternalServerError, "register: "+err.Error())
			return
		}
	}
	if ignoreWarn != "" {
		if resp.Warning != "" {
			resp.Warning += "; "
		}
		resp.Warning += ignoreWarn
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *apiHandler) listProjects(w http.ResponseWriter, r *http.Request) {
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
	workloads, err := store.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	activeByProject := map[string]int{}
	for _, wl := range workloads {
		if containerStatus(wl) == "running" {
			activeByProject[wl.Project]++
		}
	}

	out := make([]apiProjectListItem, 0, len(projects))
	for _, p := range projects {
		_, mErr := manifest.Load(p.RepoPath)
		out = append(out, apiProjectListItem{
			Name:            p.Name,
			RepoPath:        p.RepoPath,
			RegisteredAt:    p.RegisteredAt.UTC().Format("2006-01-02T15:04:05Z"),
			HasManifest:     mErr == nil,
			ActiveWorkloads: activeByProject[p.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	p, err := store.GetProject(name)
	if err != nil {
		if errors.Is(err, state.ErrProjectNotFound) {
			writeAPIError(w, http.StatusNotFound, "project "+name+" not registered")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := apiProject{
		Name:         p.Name,
		RepoPath:     p.RepoPath,
		RegisteredAt: p.RegisteredAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if m, err := manifest.Load(p.RepoPath); err == nil {
		out.HasManifest = true
		out.Manifest = m
	}

	workloads, err := store.List()
	if err == nil {
		for _, wl := range workloads {
			if wl.Project != p.Name {
				continue
			}
			if containerStatus(wl) == "running" {
				out.ActiveWorkloads++
			}
		}
	}

	if entries, err := worktree.List(p.RepoPath); err == nil {
		out.WorktreeCount = len(entries)
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	if err := store.UnregisterProject(name); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name":   name,
		"status": "unregistered",
	})
}

func (h *apiHandler) listProjectWorktrees(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	p, err := store.GetProject(name)
	if err != nil {
		if errors.Is(err, state.ErrProjectNotFound) {
			writeAPIError(w, http.StatusNotFound, "project "+name+" not registered")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries, err := worktree.List(p.RepoPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "git worktree list: "+err.Error())
		return
	}

	// Match workloads to worktrees by path: the primary worktree's slug
	// often differs from its directory basename (slug = branch on `pier
	// up` from the primary), so basename-matching gives false negatives.
	// worktree_path is stable on every state row.
	workloads, _ := store.List()
	byPath := map[string]*state.Workload{}
	for _, wl := range workloads {
		if wl.Project == p.Name {
			byPath[wl.WorktreePath] = wl
		}
	}

	out := make([]apiWorktreeListItem, 0, len(entries))
	for _, e := range entries {
		item := apiWorktreeListItem{
			Path:   e.Path,
			Slug:   filepath.Base(e.Path),
			Branch: e.Branch,
		}
		if wl, ok := byPath[e.Path]; ok {
			// Surface the workload's slug — it's what the user typed in
			// `pier up` and what the action endpoints expect.
			item.Slug = wl.Slug
			item.HasWorkload = true
			view := buildAPIWorkload(h.cfg, wl)
			item.Workload = &view
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveProjectRepo looks up the repo path for a project name in the
// registry. Returns "" + nil error when name is empty (caller is using
// the legacy `repo` field). Returns ErrProjectNotFound when the name is
// non-empty but unknown.
func (h *apiHandler) resolveProjectRepo(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		return "", err
	}
	defer store.Close()
	p, err := store.GetProject(name)
	if err != nil {
		return "", err
	}
	return p.RepoPath, nil
}

