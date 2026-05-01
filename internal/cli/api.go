package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
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

// register mounts the v1 endpoints on mux.
func (h *apiHandler) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workloads", h.listWorkloads)
	mux.HandleFunc("GET /api/v1/workloads/{project}/{slug}", h.getWorkload)
	mux.HandleFunc("GET /api/v1/config", h.getConfig)
	mux.HandleFunc("GET /api/v1/doctor", h.getDoctor)
	mux.HandleFunc("POST /api/v1/workloads/{project}/{slug}/up", h.postWorkloadUp)
	mux.HandleFunc("POST /api/v1/workloads/{project}/{slug}/down", h.postWorkloadDown)
	mux.HandleFunc("GET /api/v1/workloads/{project}/{slug}/logs", h.streamWorkloadLogs)
	mux.HandleFunc("POST /api/v1/worktrees", h.postWorktree)
	mux.HandleFunc("DELETE /api/v1/worktrees/{slug}", h.deleteWorktree)
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("GET /api/v1/projects/{name}", h.getProject)
	mux.HandleFunc("DELETE /api/v1/projects/{name}", h.deleteProject)
	mux.HandleFunc("GET /api/v1/projects/{name}/manifest", h.getProjectManifest)
	mux.HandleFunc("PUT /api/v1/projects/{name}/manifest", h.putProjectManifest)
	mux.HandleFunc("GET /api/v1/projects/{name}/compose", h.getProjectCompose)
	mux.HandleFunc("GET /api/v1/projects/{name}/overlay", h.getProjectOverlay)
	mux.HandleFunc("GET /api/v1/openapi.json", h.getOpenAPI)
	mux.HandleFunc("GET /api/docs", h.getDocs)
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

// streamWorkloadLogs proxies `docker compose logs` to the response body
// with chunked transfer. follow=true keeps the stream open until the
// client disconnects — at which point r.Context() cancels and the
// underlying docker process gets SIGKILL via exec.CommandContext.
func (h *apiHandler) streamWorkloadLogs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	slug := r.PathValue("slug")

	follow := r.URL.Query().Get("follow") == "true"
	tail := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 0 {
			tail = n
		}
	}

	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	wl, err := store.Get(project, slug)
	store.Close()
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound,
				"no state row for "+project+"/"+slug+"; workload is down or never started")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	info, err := worktree.DetectFrom(wl.WorktreePath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "worktree at "+wl.WorktreePath+": "+err.Error())
		return
	}

	d, err := dailyForWorktree(info, slug, w, w)
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

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush() // commit headers before docker first-line latency
	}

	// Wrap w so each docker log write is flushed end-to-end. Without this,
	// follow-mode buffers server-side until the chunked encoder fills,
	// which can hide minutes of output on a quiet workload.
	out := &lineFlushWriter{w: w, fl: flusher}

	// Wire r.Context() into the adapter so client disconnect cancels the
	// docker logs subprocess (otherwise it'd run until the workload dies).
	d.Ctx.Out = out
	d.Ctx.Err = out
	d.Ctx.Context = r.Context()

	a, err := adapter.For(d.Manifest.Stack.Kind)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Logs returns an error on client disconnect (broken pipe) or ctx
	// cancel ("signal: killed") — both are expected end-of-stream
	// conditions, not failures the caller cares about.
	_ = a.Logs(d.Ctx, follow, tail)
}

// lineFlushWriter forwards Writes to w and calls Flush after each one so
// SSE / chunked responses surface output without server-side buffering.
type lineFlushWriter struct {
	w  io.Writer
	fl http.Flusher
}

func (l *lineFlushWriter) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	if l.fl != nil {
		l.fl.Flush()
	}
	return n, err
}

// apiWorktreeCreateRequest is the body of POST /api/v1/worktrees. `repo`
// is the absolute path of the primary worktree (the API has no central
// repo registry — sillage is responsible for remembering it). `slug`
// doubles as the directory name under [worktree].dir and the workload
// slug. `branch` defaults to slug; `from` defaults to manifest base_ref
// then main/master then HEAD.
type apiWorktreeCreateRequest struct {
	Repo   string `json:"repo"`
	Slug   string `json:"slug"`
	Branch string `json:"branch,omitempty"`
	From   string `json:"from,omitempty"`
	Up     bool   `json:"up,omitempty"`
}

type apiWorktreeCreateResponse struct {
	Project      string       `json:"project"`
	Slug         string       `json:"slug"`
	Branch       string       `json:"branch"`
	WorktreePath string       `json:"worktree_path"`
	Workload     *apiWorkload `json:"workload,omitempty"` // populated when up=true
}

func (h *apiHandler) postWorktree(w http.ResponseWriter, r *http.Request) {
	var body apiWorktreeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Repo == "" || body.Slug == "" {
		writeAPIError(w, http.StatusBadRequest, "repo and slug are required")
		return
	}

	opts := wtAddOpts{
		branch: body.Branch,
		from:   body.From,
		up:     false, // up handled below so we can return the apiWorkload
	}
	abs, branch, err := createWorktreeAt(body.Repo, body.Slug, opts, io.Discard, io.Discard)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := apiWorktreeCreateResponse{
		Slug:         body.Slug,
		Branch:       branch,
		WorktreePath: abs,
	}
	if m, err := manifest.Load(abs); err == nil {
		resp.Project = m.Project.Name
	}

	if body.Up {
		info, err := worktree.DetectFrom(abs)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "post-create detect: "+err.Error())
			return
		}
		d, err := dailyForWorktree(info, body.Slug, io.Discard, io.Discard)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "post-create daily: "+err.Error())
			return
		}
		defer d.State.Close()

		if err := runUp(d, io.Discard, io.Discard); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "post-create up: "+err.Error())
			return
		}
		if wl, err := d.State.Get(d.Ctx.Project, body.Slug); err == nil {
			view := buildAPIWorkload(h.cfg, wl)
			resp.Workload = &view
			resp.Project = d.Ctx.Project
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *apiHandler) deleteWorktree(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeAPIError(w, http.StatusBadRequest,
			"?repo=<absolute primary worktree path> required")
		return
	}

	abs, err := resolveExistingWorktreePath(repo, slug)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}

	// Best-effort down. A `pier down` failure shouldn't block removal —
	// the worktree might be wedged in a state where compose can't bring
	// it down cleanly, and the user wants the dir gone anyway.
	if info, err := worktree.DetectFrom(abs); err == nil {
		if d, err := dailyForWorktree(info, slug, io.Discard, io.Discard); err == nil {
			_ = runDown(d, false, io.Discard, io.Discard)
			d.State.Close()
		}
	}

	project := ""
	if m, err := manifest.Load(repo); err == nil {
		project = m.Project.Name
	}

	// API DELETE always passes --force: sillage is non-interactive, an
	// uncommitted-changes safety check would just turn into an opaque
	// 500. CLI users still get the prompt-by-default behavior.
	if err := removeWorktreeAt(repo, abs, true, io.Discard, io.Discard); err != nil {
		// Surface the partial-removal scenario as 200 + warning rather
		// than 500: removeWorktreeAt's prune fallback already cleaned
		// git's worktree list, so the API state is consistent — only
		// the on-disk dir might linger (typical: root-owned files from
		// a distroless container, see AGENTS.md pitfall #4).
		writeJSON(w, http.StatusOK, apiActionResponse{
			Project: project, Slug: slug, Status: "removed",
			Warning: "git rm partial: " + err.Error() +
				"; check " + abs + " — root-owned files may need `sudo rm -rf`",
		})
		return
	}

	writeJSON(w, http.StatusOK, apiActionResponse{
		Project: project, Slug: slug, Status: "removed",
	})
}
