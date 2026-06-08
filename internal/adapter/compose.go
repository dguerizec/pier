package adapter

import (
	"bytes"
	"context"
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

	if err := AttachToTraefikNetwork(c); err != nil {
		return nil, fmt.Errorf("attach to %s network: %w", c.TraefikNetwork, err)
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

func (compose) Logs(c Ctx, follow bool, tail int, services []string) error {
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
	// services empty → compose streams logs from every service in the
	// project (the multi-expose default). Otherwise the trailing positional
	// args restrict the stream to those services, mirroring the
	// `docker compose logs [SERVICE...]` interface.
	args = append(args, services...)
	if _, err := composeRun(c, args, overridePath, true); err != nil {
		return fmt.Errorf("compose logs: %w", err)
	}
	return nil
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

	ctx := c.Context
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "docker", full...)
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

// AttachToTraefikNetwork connects each exposed container to the shared
// traefik network with ONLY the FQDN aliases pier owns — never the
// compose service short name (`backend`, `frontend`, …) which would
// collide across projects and worktrees sharing the same network.
//
// Why this isn't done via compose: `docker compose` auto-registers the
// service short name as a network alias on every connected network and
// offers no way to suppress that default. `docker network connect
// --alias` lets us declare exactly the aliases we want.
//
// The exposed container stays on its project's `default` network (via
// compose's auto-attach), so the short name `backend` keeps resolving
// inside the same compose project — collisions only disappear on the
// shared network.
func AttachToTraefikNetwork(c Ctx) error {
	for _, e := range c.Expose {
		container := ServiceName(c.Project, c.Slug, e.Service)
		aliases := []string{HostFor(e, c.Slug, c.BaseDomain)}
		if e.Service == c.DefaultService {
			aliases = append(aliases, AliasHost(c.Slug, c.BaseDomain))
		}
		if err := reconnectWithAliases(c.TraefikNetwork, container, aliases); err != nil {
			return fmt.Errorf("%s: %w", container, err)
		}
	}
	return nil
}

// reconnectWithAliases makes the container's membership on `network`
// idempotent: any prior attachment (with its auto-added short aliases)
// is dropped, then a fresh attach is made with the explicit aliases
// we want.
func reconnectWithAliases(network, container string, aliases []string) error {
	// Best-effort disconnect — fails silently when the container isn't
	// on the network, which is the common case on a fresh `pier up`.
	_ = exec.Command("docker", "network", "disconnect", network, container).Run()

	args := []string{"network", "connect"}
	for _, a := range aliases {
		args = append(args, "--alias", a)
	}
	args = append(args, network, container)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker network connect: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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

// exposedDetails carries the traefik / container_name plumbing pier emits
// for an [[expose]]'d service. AliasRule is non-empty only on the
// default service (the one Stack.Service points at), where pier emits a
// second router matching the bare `<slug>.<base>` host.
type exposedDetails struct {
	ContainerName string
	RouterID      string
	HostRule      string
	AliasRule     string
	Port          int
}

// envEntry is one rendered KEY=value line for the compose `environment:`
// list. Stored sorted by key so override output is deterministic.
type envEntry struct {
	Key, Value string
}

// serviceOverride is the union of every section pier may emit for one
// compose service. A service can be exposed and have env injection at
// once; a non-exposed service can still need its host ports / explicit
// container_name reset to avoid colliding with sibling worktrees.
type serviceOverride struct {
	Service            string
	User               string
	Exposed            *exposedDetails
	ResetPorts         bool
	ResetContainerName bool
	Env                []envEntry
}

func renderOverride(c Ctx) ([]byte, error) {
	if c.TraefikNetwork == "" {
		return nil, errors.New("compose: TraefikNetwork is empty (Ctx not fully populated)")
	}
	if len(c.Expose) == 0 {
		return nil, errors.New("compose: at least one [[expose]] entry is required")
	}
	composeServices := scanComposeServices(c)
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())

	blocks := map[string]*serviceOverride{}
	get := func(name string) *serviceOverride {
		b, ok := blocks[name]
		if !ok {
			b = &serviceOverride{Service: name}
			blocks[name] = b
		}
		return b
	}

	for _, e := range c.Expose {
		alias := ""
		if e.Service == c.DefaultService {
			alias = AliasHost(c.Slug, c.BaseDomain)
		}
		b := get(e.Service)
		if c.Stack.MatchHostUID || c.Service[e.Service].MatchHostUID {
			b.User = user
		}
		b.Exposed = &exposedDetails{
			ContainerName: ServiceName(c.Project, c.Slug, e.Service),
			RouterID:      ServiceName(c.Project, c.Slug, e.Service),
			HostRule:      HostFor(e, c.Slug, c.BaseDomain),
			AliasRule:     alias,
			Port:          e.Port,
		}
		// Exposed services always have their host ports stripped — traefik
		// routes via the pier network, host bindings would collide between
		// worktrees. ResetContainerName isn't needed because Exposed sets
		// container_name to a pier-managed value that takes precedence.
		b.ResetPorts = true
	}

	for name, cfg := range c.Service {
		if !cfg.MatchHostUID {
			continue
		}
		if _, exists := blocks[name]; !exists && len(composeServices) > 0 {
			if _, exists := composeServices[name]; !exists {
				return nil, fmt.Errorf("service[%s].match_host_uid: compose service %q not found", name, name)
			}
		}
		get(name).User = user
	}

	for name, info := range composeServices {
		b, exists := blocks[name]
		if !exists && !info.hasPorts && !info.hasContainerName {
			continue
		}
		if !exists {
			b = get(name)
		}
		if b.Exposed == nil {
			b.ResetPorts = b.ResetPorts || info.hasPorts
			b.ResetContainerName = info.hasContainerName
		}
	}

	for service, env := range c.Env {
		expanded, err := ExpandEnvBlock(env, c)
		if err != nil {
			return nil, fmt.Errorf("env[%s]: %w", service, err)
		}
		b := get(service)
		b.Env = sortedEnv(expanded)
	}

	ordered := make([]*serviceOverride, 0, len(blocks))
	for _, b := range blocks {
		ordered = append(ordered, b)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Service < ordered[j].Service })

	t := template.Must(template.New("override").Parse(`# managed by pier — do not edit
services:
{{- range .Blocks}}
  {{.Service}}:
{{- if .User}}
    user: "{{.User}}"
{{- end}}
{{- with .Exposed}}
    container_name: {{.ContainerName}}
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
{{- end}}
{{- if and (not .Exposed) .ResetContainerName}}
    container_name: !reset null
{{- end}}
{{- if .ResetPorts}}
    ports: !reset []
{{- end}}
{{- if .Env}}
    environment:
{{- range .Env}}
      - {{.Key}}={{.Value}}
{{- end}}
{{- end}}
{{- end}}
`))
	data := struct {
		Network string
		Blocks  []*serviceOverride
	}{
		Network: c.TraefikNetwork,
		Blocks:  ordered,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render override: %w", err)
	}
	return buf.Bytes(), nil
}

// sortedEnv flattens an env map into key-sorted entries so the rendered
// override is byte-stable.
func sortedEnv(env map[string]string) []envEntry {
	if len(env) == 0 {
		return nil
	}
	out := make([]envEntry, 0, len(env))
	for k, v := range env {
		out = append(out, envEntry{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// composeServiceInfo records what we need from a compose service to decide
// whether the override must reset ports / container_name on it.
type composeServiceInfo struct {
	hasPorts         bool
	hasContainerName bool
}

// ListComposeServices returns the service names declared in the compose
// file at stackFile (absolute or interpreted relative to the caller's cwd).
// Returns nil on read or parse error — callers (today: shell completion)
// should silently skip suggestions rather than fail loudly. Unsorted; the
// caller sorts if it cares.
func ListComposeServices(stackFile string) []string {
	body, err := os.ReadFile(stackFile)
	if err != nil {
		return nil
	}
	var doc struct {
		Services map[string]struct{} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		out = append(out, name)
	}
	return out
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
