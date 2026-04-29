package cli

import (
	_ "embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
	"github.com/LeoPartt/pier/internal/state"
)

//go:embed web/page.html
var webPageHTML string

var webPageTemplate = template.Must(template.New("page").Parse(webPageHTML))

func newServeCmd() *cobra.Command {
	var (
		bind    string
		port    int
		refresh int
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

			addr := net.JoinHostPort(bind, fmt.Sprintf("%d", port))
			mux := http.NewServeMux()
			// Pin the dashboard to the exact root path so the catch-all
			// doesn't shadow /api/v1/* and break the mux's 405 handling.
			mux.Handle("GET /{$}", &webHandler{paths: paths, cfg: cfg, refresh: refresh})
			(&apiHandler{paths: paths, cfg: cfg}).register(mux)

			fmt.Fprintf(cmd.OutOrStdout(), "→ http://%s\n", addr)
			fmt.Fprintln(cmd.OutOrStdout(), "  ctrl-c to stop")
			srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			return srv.ListenAndServe()
		},
	}
	f := cmd.Flags()
	f.StringVar(&bind, "bind", "127.0.0.1", "interface to bind on")
	f.IntVar(&port, "port", 60080, "TCP port to listen on")
	f.IntVar(&refresh, "refresh", 5, "page auto-refresh interval in seconds")
	return cmd
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
	Name      string
	Workloads []webWorkload
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

	byProject := map[string][]*state.Workload{}
	for _, w := range workloads {
		byProject[w.Project] = append(byProject[w.Project], w)
	}

	var projects []webProject
	for name, ws := range byProject {
		sort.Slice(ws, func(i, j int) bool { return ws[i].Slug < ws[j].Slug })
		project := webProject{Name: name}
		for _, w := range ws {
			project.Workloads = append(project.Workloads, h.workloadView(w))
		}
		projects = append(projects, project)
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
