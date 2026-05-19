// Package adapter abstracts how a workload is brought up, taken down, and
// inspected. compose is the only kind in v0.2; dockerfile (synthesized
// compose) lands in Phase 3. The process kind from DESIGN §5.5 was
// dropped — pier is intentionally docker-coupled, even for raw-process
// stacks (uv/npm/cargo), because it keeps a single execution path,
// avoids host port/PID/log management, and works on any platform docker
// supports. See README's "minimal compose" example.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Ctx carries everything an adapter needs to run a command.
type Ctx struct {
	Project        string                       // manifest.project.name
	Slug           string                       // derived or overridden DNS slug
	BaseDomain     string                       // manifest.project.base_domain (post-template expansion)
	TLD            string                       // installed pier TLD; exposed via {pier.tld} in templates
	WorktreePath   string                       // git toplevel
	Stack          manifest.Stack               // adapter-specific fields under manifest.stack
	Expose         []manifest.ExposeRule        // services pier should publish behind traefik
	DefaultService string                       // service name that gets the bare-slug alias; "" when no alias
	Env            map[string]map[string]string // service → key → templated value injected into the override
	TraefikNetwork string                       // docker network the workload joins for label discovery
	Out            io.Writer                    // command output sink
	Err            io.Writer                    // command error sink
	Context        context.Context              // cancels the underlying docker process; nil = no cancellation
}

// Handle is the state to persist after a successful Up.
type Handle struct {
	ContainerID string // compose / dockerfile (first exposed service)
}

// Adapter is implemented per stack kind.
type Adapter interface {
	Up(c Ctx) (*Handle, error)
	Down(c Ctx) error
	Logs(c Ctx, follow bool, tail int, services []string) error
}

// ErrUnsupportedKind signals a manifest with a stack.kind we cannot run yet.
var ErrUnsupportedKind = errors.New("adapter: unsupported stack kind")

// For returns the adapter for kind.
func For(kind string) (Adapter, error) {
	switch kind {
	case manifest.KindCompose:
		return &compose{}, nil
	case manifest.KindDockerfile:
		return nil, fmt.Errorf("%w: %q — dockerfile adapter (synthesized compose) lands in Phase 3. Add a docker-compose.dev.yml that uses your Dockerfile (`build: .`) for now.", ErrUnsupportedKind, kind)
	default:
		return nil, fmt.Errorf("%w: %q (only `compose` is supported)", ErrUnsupportedKind, kind)
	}
}

// Name is the workload's compose project name (-p value).
func Name(project, slug string) string { return project + "-" + slug }

// ServiceName is the per-service identifier used for container_name and
// traefik router/service IDs. Adding the service suffix keeps everything
// unique per (project, slug, service) so multi-expose workloads don't
// collide on names.
func ServiceName(project, slug, service string) string {
	return Name(project, slug) + "-" + service
}

// HostFor returns the fully qualified host an exposed service is reachable
// at: `<host>.<slug>.<base_domain>` where <host> is rule.Hostname().
func HostFor(rule manifest.ExposeRule, slug, baseDomain string) string {
	return rule.Hostname() + "." + slug + "." + baseDomain
}

// AliasHost returns the bare `<slug>.<base_domain>` reserved for the
// service marked default by Stack.Service.
func AliasHost(slug, baseDomain string) string {
	return slug + "." + baseDomain
}

// URLs returns every public URL the workload answers on, default-first
// when present. Used by `pier up`, `pier url --all`, and `pier ls`.
func URLs(c Ctx) []string {
	var out []string
	if c.DefaultService != "" {
		out = append(out, "http://"+AliasHost(c.Slug, c.BaseDomain))
	}
	for _, e := range c.Expose {
		out = append(out, "http://"+HostFor(e, c.Slug, c.BaseDomain))
	}
	return out
}

// DefaultURL returns the URL pier prints by default in `pier url`. When
// Stack.Service designates an exposed service, that's the bare-slug
// alias; otherwise we fall back to the first expose's host so the
// command always returns something useful.
func DefaultURL(c Ctx) string {
	if c.DefaultService != "" {
		return "http://" + AliasHost(c.Slug, c.BaseDomain)
	}
	if len(c.Expose) > 0 {
		return "http://" + HostFor(c.Expose[0], c.Slug, c.BaseDomain)
	}
	return ""
}

