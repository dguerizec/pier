package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
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
		Short:   "Serve a small web dashboard listing running workloads and their URLs",
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
			handler := &webHandler{
				paths:   paths,
				cfg:     cfg,
				refresh: refresh,
			}
			mux := http.NewServeMux()
			mux.Handle("/", handler)

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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := h.buildPage()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := webPageTemplate.Execute(w, page); err != nil {
		// Headers already sent — best we can do is log.
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
	URLs       []webURL
	Containers []webContainer
	ErrMsg     string
}

type webURL struct {
	URL     string
	Label   string
	Default bool
}

type webContainer struct {
	Name   string
	Image  string
	Status string
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
		view.URLs = h.urlsFor(w, m)
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

func (h *webHandler) urlsFor(w *state.Workload, m *manifest.Manifest) []webURL {
	baseDomain := m.Project.BaseDomain
	if baseDomain == "" {
		baseDomain = m.Project.Name + "." + h.cfg.TLD
	} else if expanded, err := adapter.ExpandPierTokens(baseDomain, h.cfg.TLD); err == nil {
		baseDomain = expanded
	}

	defaultService := ""
	if d := m.DefaultExpose(); d != nil {
		defaultService = d.Service
	}

	var out []webURL
	if defaultService != "" {
		alias := adapter.AliasHost(w.Slug, baseDomain)
		out = append(out, webURL{URL: "http://" + alias, Label: alias, Default: true})
	}
	for _, e := range m.Expose {
		host := adapter.HostFor(e, w.Slug, baseDomain)
		out = append(out, webURL{URL: "http://" + host, Label: host})
	}
	return out
}

// listProjectContainers asks docker for every container labelled with the
// given compose project. The format keeps the columns stable — we parse
// JSON because `--format` text would split on spaces in image refs.
func listProjectContainers(projectName string) ([]webContainer, error) {
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project="+projectName,
		"--format", "{{json .}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var containers []webContainer
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
		containers = append(containers, webContainer{
			Name:   entry.Names,
			Image:  entry.Image,
			Status: entry.State,
		})
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}
