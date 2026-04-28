package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const (
	overrideSubdir = ".pier"
	overrideFile   = "compose.override.yml"
)

type compose struct{}

func (compose) Up(c Ctx) (*Handle, error) {
	if c.Stack.File == "" {
		return nil, errors.New("compose: stack.file is required")
	}
	if len(c.Expose) == 0 {
		return nil, errors.New("compose: at least one [[expose]] entry is required")
	}

	overridePath, err := writeOverride(c)
	if err != nil {
		return nil, err
	}

	if err := ensureExternalNetworks(c, overridePath); err != nil {
		fmt.Fprintf(c.Err, "warning: ensure external networks: %v\n", err)
	}

	if _, err := composeRun(c, []string{"up", "-d", "--build"}, overridePath, true); err != nil {
		return nil, fmt.Errorf("compose up: %w", err)
	}

	// We track the default exposed service's container id (or the first
	// expose when no default is set) as the workload's "primary" container.
	// state stays single-row per workload, sufficient for ls/down today.
	primary := c.DefaultService
	if primary == "" {
		primary = c.Expose[0].Service
	}
	cid, err := composeContainerID(c, overridePath, primary)
	if err != nil {
		fmt.Fprintf(c.Err, "warning: could not resolve container id (%v)\n", err)
	}
	return &Handle{ContainerID: cid}, nil
}

func (compose) Down(c Ctx) error {
	overridePath := filepath.Join(c.WorktreePath, overrideSubdir, overrideFile)
	if _, err := os.Stat(overridePath); errors.Is(err, os.ErrNotExist) {
		// Regenerate the override so `down` works even on a fresh checkout
		// where the .pier directory has been removed.
		var werr error
		overridePath, werr = writeOverride(c)
		if werr != nil {
			return werr
		}
	}
	if _, err := composeRun(c, []string{"down"}, overridePath, true); err != nil {
		return fmt.Errorf("compose down: %w", err)
	}
	return nil
}

func (compose) Logs(c Ctx, follow bool, tail int) error {
	overridePath := filepath.Join(c.WorktreePath, overrideSubdir, overrideFile)
	if _, err := os.Stat(overridePath); errors.Is(err, os.ErrNotExist) {
		var werr error
		overridePath, werr = writeOverride(c)
		if werr != nil {
			return werr
		}
	}
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	// No service argument → compose streams logs from every service in the
	// project. Multi-expose makes this the right default; users who want a
	// single service can `docker compose -p <name> logs <svc>` directly.
	_, err := composeRun(c, args, overridePath, true)
	return err
}

// composeRun shells out to `docker compose -f <stack> -f <override> -p <name> <args...>`.
// When stream is true, stdout/stderr are forwarded to the caller's writers.
func composeRun(c Ctx, args []string, overridePath string, stream bool) (string, error) {
	stackPath := stackFilePath(c)
	full := []string{
		"compose",
		"-f", stackPath,
		"-f", overridePath,
		"-p", Name(c.Project, c.Slug),
	}
	full = append(full, args...)

	cmd := exec.Command("docker", full...)
	cmd.Dir = c.WorktreePath
	if stream {
		cmd.Stdout = c.Out
		cmd.Stderr = c.Err
		err := cmd.Run()
		return "", err
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = c.Err
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func composeContainerID(c Ctx, overridePath, service string) (string, error) {
	cmd := exec.Command("docker",
		"compose",
		"-f", stackFilePath(c),
		"-f", overridePath,
		"-p", Name(c.Project, c.Slug),
		"ps", "-q", service,
	)
	cmd.Dir = c.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("compose ps returned no container id")
	}
	// Multiple replicas would yield multi-line output; pier treats the
	// first as canonical until we add scale-aware status.
	return strings.SplitN(id, "\n", 2)[0], nil
}

func stackFilePath(c Ctx) string {
	if filepath.IsAbs(c.Stack.File) {
		return c.Stack.File
	}
	return filepath.Join(c.WorktreePath, c.Stack.File)
}

// writeOverride renders the pier-managed compose override under
// <worktree>/.pier/compose.override.yml and returns its path.
func writeOverride(c Ctx) (string, error) {
	dir := filepath.Join(c.WorktreePath, overrideSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, overrideFile)

	body, err := renderOverride(c)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// ensureExternalNetworks renders the merged compose config and creates any
// `external: true` network the user's compose file references but the
// docker daemon doesn't have. The pier network itself is provisioned at
// install time so the existence check below is a no-op for it.
//
// Best-effort: a compose config failure here means up will surface the
// real error a moment later — we don't want to mask it.
func ensureExternalNetworks(c Ctx, overridePath string) error {
	cmd := exec.Command("docker",
		"compose",
		"-f", stackFilePath(c),
		"-f", overridePath,
		"config", "--format=json",
	)
	cmd.Dir = c.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var cfg struct {
		Networks map[string]struct {
			External any    `json:"external"`
			Name     string `json:"name"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return nil
	}
	for _, n := range cfg.Networks {
		if !isExternal(n.External) {
			continue
		}
		netName := n.Name
		if netName == "" {
			continue
		}
		if err := ensureDockerNetwork(netName); err != nil {
			fmt.Fprintf(c.Err, "warning: create network %s: %v\n", netName, err)
		}
	}
	return nil
}

// isExternal handles both the modern `external: true` form and the older
// `external: {name: "..."}` form compose still accepts.
func isExternal(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case map[string]any:
		return len(t) > 0
	}
	return false
}

func ensureDockerNetwork(name string) error {
	out, err := exec.Command("docker", "network", "ls",
		"--filter", "name=^"+name+"$", "--format", "{{.Name}}").Output()
	if err == nil && strings.TrimSpace(string(out)) == name {
		return nil
	}
	if _, err := exec.Command("docker", "network", "create", name).CombinedOutput(); err != nil {
		return err
	}
	return nil
}

// exposedBlock describes one [[expose]]'d service rendered into the
// override: pier-managed container_name, traefik labels (one or two
// routers), host ports stripped, networks attached. Two routers when this
// is the default service — the second matches the bare `<slug>.<base>`.
type exposedBlock struct {
	Service       string // compose service name (YAML key)
	ContainerName string // pier-managed name
	RouterID      string // traefik router/service id
	HostRule      string // primary Host(`...`) target
	AliasRule     string // bare-slug Host(`...`); empty when not the default
	Port          int
}

// resetBlock describes a non-exposed service that needs its host bindings
// or explicit container_name removed so multi-worktree runs don't collide.
type resetBlock struct {
	Service            string
	ResetPorts         bool
	ResetContainerName bool
}

func renderOverride(c Ctx) ([]byte, error) {
	if c.TraefikNetwork == "" {
		return nil, errors.New("compose: TraefikNetwork is empty (Ctx not fully populated)")
	}
	if len(c.Expose) == 0 {
		return nil, errors.New("compose: at least one [[expose]] entry is required")
	}

	exposed := make([]exposedBlock, 0, len(c.Expose))
	exposedSet := make(map[string]bool, len(c.Expose))
	for _, e := range c.Expose {
		exposedSet[e.Service] = true
		host := HostFor(e, c.Slug, c.BaseDomain)
		alias := ""
		if e.Service == c.DefaultService {
			alias = AliasHost(c.Slug, c.BaseDomain)
		}
		exposed = append(exposed, exposedBlock{
			Service:       e.Service,
			ContainerName: ServiceName(c.Project, c.Slug, e.Service),
			RouterID:      ServiceName(c.Project, c.Slug, e.Service),
			HostRule:      host,
			AliasRule:     alias,
			Port:          e.Port,
		})
	}
	sort.Slice(exposed, func(i, j int) bool { return exposed[i].Service < exposed[j].Service })

	var resets []resetBlock
	for name, info := range scanComposeServices(c) {
		if exposedSet[name] {
			continue
		}
		if !info.hasPorts && !info.hasContainerName {
			continue
		}
		resets = append(resets, resetBlock{
			Service:            name,
			ResetPorts:         info.hasPorts,
			ResetContainerName: info.hasContainerName,
		})
	}
	sort.Slice(resets, func(i, j int) bool { return resets[i].Service < resets[j].Service })

	t := template.Must(template.New("override").Parse(`# managed by pier — do not edit
services:
{{- range .Exposed}}
  {{.Service}}:
    container_name: {{.ContainerName}}
{{- if $.User}}
    user: "{{$.User}}"
{{- end}}
    labels:
      - traefik.enable=true
      - traefik.http.routers.{{.RouterID}}.rule=Host(` + "`{{.HostRule}}`" + `)
      - traefik.http.routers.{{.RouterID}}.entrypoints=web
      - traefik.http.routers.{{.RouterID}}.service={{.RouterID}}
{{- if .AliasRule}}
      - traefik.http.routers.{{.RouterID}}-default.rule=Host(` + "`{{.AliasRule}}`" + `)
      - traefik.http.routers.{{.RouterID}}-default.entrypoints=web
      - traefik.http.routers.{{.RouterID}}-default.service={{.RouterID}}
{{- end}}
      - traefik.docker.network={{$.Network}}
      - traefik.http.services.{{.RouterID}}.loadbalancer.server.port={{.Port}}
    networks: [default, {{$.Network}}]
    ports: !reset []
{{- end}}
{{- range .Resets}}
  {{.Service}}:
{{- if .ResetContainerName}}
    container_name: !reset null
{{- end}}
{{- if .ResetPorts}}
    ports: !reset []
{{- end}}
{{- end}}

networks:
  {{$.Network}}:
    external: true
`))
	user := ""
	if c.Stack.MatchHostUID {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	data := struct {
		Network, User string
		Exposed       []exposedBlock
		Resets        []resetBlock
	}{
		Network: c.TraefikNetwork,
		User:    user,
		Exposed: exposed,
		Resets:  resets,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render override: %w", err)
	}
	return buf.Bytes(), nil
}

// composeServiceInfo records what we need from a compose service to decide
// whether the override must reset ports / container_name on it.
type composeServiceInfo struct {
	hasPorts         bool
	hasContainerName bool
}

// scanComposeServices reads the user's compose file and reports per-service
// whether it declares host port bindings or an explicit container_name. We
// need both: ports collide between worktrees on the host network, and an
// explicit container_name in the user's file overrides docker compose's
// project-prefixed default and would also collide.
//
// Returns nil when the file is missing or unreadable — pier still renders
// the exposed block and lets compose surface the real error a moment later.
func scanComposeServices(c Ctx) map[string]composeServiceInfo {
	body, err := os.ReadFile(stackFilePath(c))
	if err != nil {
		return nil
	}
	var doc struct {
		Services map[string]struct {
			Ports         []any  `yaml:"ports"`
			ContainerName string `yaml:"container_name"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := make(map[string]composeServiceInfo, len(doc.Services))
	for name, svc := range doc.Services {
		out[name] = composeServiceInfo{
			hasPorts:         len(svc.Ports) > 0,
			hasContainerName: svc.ContainerName != "",
		}
	}
	return out
}
