package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
	"github.com/LeoPartt/pier/internal/worktree"
)

// JSON shapes under /api/v1/* are a contract. Any breaking change must
// land at /v2/, not as a silent edit. Adding new fields is fine; removing
// or renaming existing ones is not.

type apiURL struct {
	URL     string `json:"url"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

type apiContainer struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

type apiWorkload struct {
	Project       string         `json:"project"`
	Slug          string         `json:"slug"`
	Branch        string         `json:"branch"`
	Kind          string         `json:"kind"`
	Status        string         `json:"status"`
	URLs          []apiURL       `json:"urls"`
	Containers    []apiContainer `json:"containers"`
	WorktreePath  string         `json:"worktree_path"`
	ContainerID   string         `json:"container_id,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Error         string         `json:"error,omitempty"`
}

type apiConfig struct {
	Mode             string `json:"mode"`
	TLD              string `json:"tld"`
	BindIP           string `json:"bind_ip"`
	AnswerIP         string `json:"answer_ip"`
	TraefikNetwork   string `json:"traefik_network"`
	ExternalTraefik  string `json:"external_traefik,omitempty"`
	HeadscaleRecords string `json:"headscale_records_path,omitempty"`
	Version          string `json:"version"`
}

type apiCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass" | "warn" | "fail"
	Detail  string `json:"detail,omitempty"`
	FixHint string `json:"fix_hint,omitempty"`
}

type apiDoctorReport struct {
	Failed bool       `json:"failed"`
	Checks []apiCheck `json:"checks"`
}

type apiHandler struct {
	paths *infra.Paths
	cfg   *infra.Config
	hub   *eventHub // nil = no SSE endpoint registered (used in tests)
}

// register mounts the v1 endpoints on mux. Phase 3 worktree CRUD will
// hang off the same handler.
func (h *apiHandler) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workloads", h.listWorkloads)
	mux.HandleFunc("GET /api/v1/workloads/{project}/{slug}", h.getWorkload)
	mux.HandleFunc("GET /api/v1/config", h.getConfig)
	mux.HandleFunc("GET /api/v1/doctor", h.getDoctor)
	mux.HandleFunc("POST /api/v1/workloads/{project}/{slug}/up", h.postWorkloadUp)
	mux.HandleFunc("POST /api/v1/workloads/{project}/{slug}/down", h.postWorkloadDown)
	if h.hub != nil {
		mux.HandleFunc("GET /api/v1/events", h.streamEvents)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *apiHandler) listWorkloads(w http.ResponseWriter, r *http.Request) {
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	list, err := store.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Project != list[j].Project {
			return list[i].Project < list[j].Project
		}
		return list[i].Slug < list[j].Slug
	})

	out := make([]apiWorkload, 0, len(list))
	for _, wl := range list {
		out = append(out, buildAPIWorkload(h.cfg, wl))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) getWorkload(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	slug := r.PathValue("slug")
	if project == "" || slug == "" {
		writeAPIError(w, http.StatusBadRequest, "missing project or slug")
		return
	}
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer store.Close()

	wl, err := store.Get(project, slug)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("workload %s/%s not found", project, slug))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildAPIWorkload(h.cfg, wl))
}

func (h *apiHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, apiConfig{
		Mode:             h.cfg.Mode,
		TLD:              h.cfg.TLD,
		BindIP:           h.cfg.BindIP,
		AnswerIP:         h.cfg.EffectiveAnswerIP(),
		TraefikNetwork:   h.cfg.EffectiveTraefikNetwork(),
		ExternalTraefik:  h.cfg.ExternalTraefik,
		HeadscaleRecords: h.cfg.HeadscaleRecordsPath,
		Version:          version,
	})
}

func (h *apiHandler) getDoctor(w http.ResponseWriter, r *http.Request) {
	report := infra.Diagnose()
	appendStateChecks(&report)

	out := apiDoctorReport{
		Failed: report.HasFailures(),
		Checks: make([]apiCheck, 0, len(report.Checks)),
	}
	for _, c := range report.Checks {
		out.Checks = append(out.Checks, apiCheck{
			Name:    c.Name,
			Status:  statusString(c.Status),
			Detail:  c.Detail,
			FixHint: c.FixHint,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func statusString(s infra.Status) string {
	switch s {
	case infra.StatusPass:
		return "pass"
	case infra.StatusWarn:
		return "warn"
	case infra.StatusFail:
		return "fail"
	}
	return "unknown"
}

// buildAPIWorkload assembles the JSON view of a workload — manifest URLs
// + live container info + uptime. Mirrors webHandler.workloadView so the
// dashboard and the API never disagree.
func buildAPIWorkload(cfg *infra.Config, wl *state.Workload) apiWorkload {
	out := apiWorkload{
		Project:       wl.Project,
		Slug:          wl.Slug,
		Branch:        wl.Branch,
		Kind:          wl.Kind,
		Status:        containerStatus(wl),
		WorktreePath:  wl.WorktreePath,
		ContainerID:   wl.ContainerID,
		StartedAt:     wl.StartedAt,
		UptimeSeconds: int64(time.Since(wl.StartedAt).Seconds()),
		URLs:          []apiURL{},
		Containers:    []apiContainer{},
	}

	m, err := manifest.Load(wl.WorktreePath)
	if err != nil {
		out.Error = "manifest unreadable: " + err.Error()
	} else {
		if urls := workloadURLs(cfg, wl, m); urls != nil {
			out.URLs = urls
		}
	}

	containers, cerr := listProjectContainers(adapter.Name(wl.Project, wl.Slug))
	if cerr != nil {
		if out.Error == "" {
			out.Error = "docker ps: " + cerr.Error()
		}
	} else if containers != nil {
		out.Containers = containers
	}
	return out
}

// listProjectContainers asks docker for every container labelled with the
// given compose project. The format keeps the columns stable — we parse
// JSON because `--format` text would split on spaces in image refs.
func listProjectContainers(projectName string) ([]apiContainer, error) {
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project="+projectName,
		"--format", "{{json .}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var containers []apiContainer
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Names string `json:"Names"`
			Image string `json:"Image"`
			State string `json:"State"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		containers = append(containers, apiContainer{
			Name:   entry.Names,
			Image:  entry.Image,
			Status: entry.State,
		})
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}

// apiUpRequest is the optional body of POST /up. Sillage uses
// worktree_path on the first call after a `pier down` (which dropped the
// state row), since pier doesn't keep a persistent (project, slug) →
// path index outside state. Subsequent calls can omit it; the state row
// from the previous up provides the path.
type apiUpRequest struct {
	WorktreePath string `json:"worktree_path,omitempty"`
}

// apiActionResponse is the lightweight POST /down response. POST /up
// returns a full apiWorkload — there's no apiWorkload to return for a
// just-stopped workload (the state row is gone), so we surface just
// enough for sillage to confirm what happened.
type apiActionResponse struct {
	Project string `json:"project"`
	Slug    string `json:"slug"`
	Status  string `json:"status"`            // "running" | "down"
	Warning string `json:"warning,omitempty"` // surfaced when the row was dropped despite the worktree being gone
}

func (h *apiHandler) postWorkloadUp(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	slug := r.PathValue("slug")

	var body apiUpRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}

	path := body.WorktreePath
	if path == "" {
		// Look up the existing state row to recover the worktree path.
		// Missing row + missing body field = caller hasn't told us where
		// the worktree lives, and pier has no central registry.
		store, err := state.Open(h.paths.StateDB)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		wl, err := store.Get(project, slug)
		store.Close()
		if err == nil {
			path = wl.WorktreePath
		}
	}
	if path == "" {
		writeAPIError(w, http.StatusBadRequest,
			"no state row for "+project+"/"+slug+
				"; provide worktree_path in the request body, or POST /api/v1/worktrees first")
		return
	}

	info, err := worktree.DetectFrom(path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "worktree at "+path+": "+err.Error())
		return
	}

	d, err := dailyForWorktree(info, slug, io.Discard, io.Discard)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer d.State.Close()

	if d.Manifest.Project.Name != project {
		writeAPIError(w, http.StatusConflict,
			fmt.Sprintf("manifest at %s declares project=%q, URL says %q",
				info.Toplevel, d.Manifest.Project.Name, project))
		return
	}

	// Idempotent up: if a row already exists and its container is
	// running, return the current state without touching docker. Mirrors
	// `docker compose up -d` no-op semantics — sillage retries are safe.
	if existing, err := d.State.Get(project, slug); err == nil {
		if containerStatus(existing) == "running" {
			writeJSON(w, http.StatusOK, buildAPIWorkload(h.cfg, existing))
			return
		}
	}

	if err := runUp(d, io.Discard, io.Discard); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "up failed: "+err.Error())
		return
	}

	wl, err := d.State.Get(project, slug)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "post-up state read: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildAPIWorkload(h.cfg, wl))
}

func (h *apiHandler) postWorkloadDown(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	slug := r.PathValue("slug")

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	wl, err := store.Get(project, slug)
	store.Close()
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			// Already down — idempotent success.
			writeJSON(w, http.StatusOK, apiActionResponse{
				Project: project, Slug: slug, Status: "down",
			})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	info, err := worktree.DetectFrom(wl.WorktreePath)
	if err != nil {
		// Worktree gone (deleted out from under pier). The containers
		// might still exist; we can't drive compose without a manifest,
		// so just drop the orphaned row and warn the caller.
		s, e := state.Open(h.paths.StateDB)
		if e == nil {
			_ = s.Delete(project, slug)
			s.Close()
		}
		writeJSON(w, http.StatusOK, apiActionResponse{
			Project: project, Slug: slug, Status: "down",
			Warning: "worktree at " + wl.WorktreePath + " missing; state row dropped without docker compose down",
		})
		return
	}

	d, err := dailyForWorktree(info, slug, io.Discard, io.Discard)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer d.State.Close()

	if err := runDown(d, false, io.Discard, io.Discard); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "down failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiActionResponse{
		Project: project, Slug: slug, Status: "down",
	})
}
