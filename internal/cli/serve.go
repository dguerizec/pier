package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/headscale"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
)

//go:embed web/page.html
var webPageHTML string

var webPageTemplate = template.Must(template.New("page").Parse(webPageHTML))

func newServeCmd() *cobra.Command {
	var (
		bind        string
		port        int
		refresh     int
		corsOrigins []string
	)
	cmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"web"},
		Short:   "Serve the pier HTTP surface (dashboard at /, REST API at /api/v1/)",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := infra.DefaultPaths()
			if err != nil {
				return err
			}
			cfg, err := infra.LoadConfig(paths)
			if err != nil {
				return err
			}

			effectiveBind := bind
			if effectiveBind == "" {
				effectiveBind = autoBind(cfg)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			hub := newEventHub(paths, cfg)
			hub.start(ctx)

			mux := http.NewServeMux()
			mux.Handle("GET /{$}", &webHandler{paths: paths, cfg: cfg, refresh: refresh})
			(&apiHandler{paths: paths, cfg: cfg, hub: hub}).register(mux)

			handler := withCORS(mux, corsOrigins)

			out := cmd.OutOrStdout()
			recordName, recordRegistered := registerDashboardRecord(cfg, effectiveBind, out)

			addr := net.JoinHostPort(effectiveBind, fmt.Sprintf("%d", port))
			srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

			fmt.Fprintf(out, "→ http://%s\n", addr)
			if recordRegistered {
				fmt.Fprintf(out, "→ http://%s:%d\n", recordName, port)
			}
			fmt.Fprintln(out, "  ctrl-c to stop")

			go func() {
				<-ctx.Done()
				if recordRegistered {
					if removed, err := headscale.Remove(cfg.HeadscaleRecordsPath, recordName); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "! headscale record cleanup %s: %v\n", recordName, err)
					} else if removed {
						fmt.Fprintf(out, "✓ headscale record removed: %s\n", recordName)
					}
				}
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			err = srv.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&bind, "bind", "", "interface to bind on (default: tailnet IP in server+records mode, else 127.0.0.1)")
	f.IntVar(&port, "port", 60080, "TCP port to listen on")
	f.IntVar(&refresh, "refresh", 5, "dashboard auto-refresh interval in seconds")
	f.StringSliceVar(&corsOrigins, "cors-origin", []string{"*"}, "comma-separated CORS origins for /api/v1/* (default: any)")
	return cmd
}

// autoBind picks the right interface when --bind isn't given. In a
// server+records install pier already knows the tailnet-reachable IP
// (AnswerIP), so binding on it makes the dashboard + API reachable from
// peers without extra config. Anything else falls back to localhost so a
// laptop install doesn't accidentally expose itself.
func autoBind(cfg *infra.Config) string {
	if cfg.Mode == infra.ModeServer && cfg.HeadscaleRecordsPath != "" && cfg.EffectiveAnswerIP() != "" {
		return cfg.EffectiveAnswerIP()
	}
	return "127.0.0.1"
}

// registerDashboardRecord adds a `pier.<tld>` A record so peers can reach
// the dashboard at a friendly URL instead of an IP. No-op outside records
// mode, when bind is loopback (the address wouldn't be reachable anyway),
// or when the record already exists for someone else (ErrConflict).
func registerDashboardRecord(cfg *infra.Config, bind string, out io.Writer) (string, bool) {
	if cfg.HeadscaleRecordsPath == "" || cfg.TLD == "" {
		return "", false
	}
	if bind == "" || bind == "127.0.0.1" || bind == "0.0.0.0" {
		return "", false
	}
	name := "pier." + cfg.TLD
	added, err := headscale.Add(cfg.HeadscaleRecordsPath, name, bind)
	if errors.Is(err, headscale.ErrConflict) {
		fmt.Fprintf(out, "! headscale: %s already mapped elsewhere; skipping auto-record\n", name)
		return "", false
	}
	if err != nil {
		fmt.Fprintf(out, "! headscale auto-record %s: %v\n", name, err)
		return "", false
	}
	if added {
		fmt.Fprintf(out, "✓ headscale record: %s → %s\n", name, bind)
	}
	// Even when the record already existed (added=false, no conflict),
	// we still own the cleanup — return registered=true so shutdown
	// removes it. Rationale: a previous serve run left the record
	// behind on a crash; the running daemon should still tidy up.
	return name, true
}

// withCORS injects Access-Control-* headers on /api/v1/* responses and
// short-circuits OPTIONS preflights. Origins is a static allowlist; "*"
// (the MVP default) means any. The dashboard at / is same-origin and
// doesn't need CORS, so we scope the middleware to /api/.
func withCORS(next http.Handler, origins []string) http.Handler {
	wildcard := len(origins) == 1 && origins[0] == "*"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		switch {
		case wildcard:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && slices.Contains(origins, origin):
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type webHandler struct {
	paths   *infra.Paths
	cfg     *infra.Config
	refresh int
}

func (h *webHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	page, err := h.buildPage()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := webPageTemplate.Execute(w, page); err != nil {
		fmt.Fprintf(w, "\n<!-- template error: %v -->", err)
	}
}

type webPage struct {
	Now            string
	Version        string
	TLD            string
	RefreshSeconds int
	Projects       []webProject
}

type webProject struct {
	Name       string
	Path       string
	BaseDomain string
	// Registered is true when the project comes from the registry
	// (state.ListProjects). False for workload-only entries — typically
	// stale state from before the registry, or a manifest renamed
	// without re-init.
	Registered bool
	Workloads  []webWorkload
}

type webWorkload struct {
	Slug       string
	Branch     string
	Status     string
	Uptime     string
	URLs       []apiURL
	Containers []apiContainer
	ErrMsg     string
}

func (h *webHandler) buildPage() (*webPage, error) {
	store, err := state.Open(h.paths.StateDB)
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	defer store.Close()

	workloads, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("list workloads: %w", err)
	}
	registered, err := store.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	byProject := map[string][]*state.Workload{}
	for _, w := range workloads {
		byProject[w.Project] = append(byProject[w.Project], w)
	}

	seen := map[string]bool{}
	var projects []webProject
	for _, p := range registered {
		seen[p.Name] = true
		ws := byProject[p.Name]
		sort.Slice(ws, func(i, j int) bool { return ws[i].Slug < ws[j].Slug })
		view := webProject{
			Name:       p.Name,
			Path:       p.Path,
			BaseDomain: p.BaseDomain,
			Registered: true,
		}
		for _, w := range ws {
			view.Workloads = append(view.Workloads, h.workloadView(w))
		}
		projects = append(projects, view)
	}
	// Surface workloads whose project row is missing — typically state
	// rows that predate the registry. They can't be acted on as a
	// project (no path/domain), but hiding them would silently drop
	// running containers from the dashboard.
	for name, ws := range byProject {
		if seen[name] {
			continue
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Slug < ws[j].Slug })
		view := webProject{Name: name}
		for _, w := range ws {
			view.Workloads = append(view.Workloads, h.workloadView(w))
		}
		projects = append(projects, view)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })

	return &webPage{
		Now:            time.Now().Format("2006-01-02 15:04:05"),
		Version:        version,
		TLD:            h.cfg.TLD,
		RefreshSeconds: h.refresh,
		Projects:       projects,
	}, nil
}

// workloadView builds the dashboard row for one workload, loading its
// manifest to derive every public URL and querying docker for the live
// container list. Errors get surfaced inline (ErrMsg) rather than failing
// the whole page — a stale state row shouldn't blank the dashboard.
func (h *webHandler) workloadView(w *state.Workload) webWorkload {
	view := webWorkload{
		Slug:   w.Slug,
		Branch: w.Branch,
		Uptime: humanUptime(time.Since(w.StartedAt)),
		Status: containerStatus(w),
	}

	m, err := manifest.Load(w.WorktreePath)
	if err != nil {
		view.ErrMsg = fmt.Sprintf("manifest unreadable: %v", err)
	} else {
		view.URLs = workloadURLs(h.cfg, w, m)
	}

	containers, cerr := listProjectContainers(adapter.Name(w.Project, w.Slug))
	if cerr != nil {
		if view.ErrMsg == "" {
			view.ErrMsg = fmt.Sprintf("docker ps: %v", cerr)
		}
	} else {
		view.Containers = containers
	}
	return view
}

// workloadURLs returns every public URL derived from a workload's manifest,
// flagged with `Default` for the alias host. Shared by the HTML dashboard
// and the REST API so both layers stay in lock-step.
func workloadURLs(cfg *infra.Config, w *state.Workload, m *manifest.Manifest) []apiURL {
	baseDomain := m.Project.BaseDomain
	if baseDomain == "" {
		baseDomain = m.Project.Name + "." + cfg.TLD
	} else if expanded, err := adapter.ExpandPierTokens(baseDomain, cfg.TLD); err == nil {
		baseDomain = expanded
	}

	defaultService := ""
	if d := m.DefaultExpose(); d != nil {
		defaultService = d.Service
	}

	var out []apiURL
	if defaultService != "" {
		alias := adapter.AliasHost(w.Slug, baseDomain)
		out = append(out, apiURL{URL: "http://" + alias, Label: alias, Default: true})
	}
	for _, e := range m.Expose {
		host := adapter.HostFor(e, w.Slug, baseDomain)
		out = append(out, apiURL{URL: "http://" + host, Label: host})
	}
	return out
}
