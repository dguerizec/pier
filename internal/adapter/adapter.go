// Package adapter abstracts how a workload is brought up, taken down, and
// inspected. compose is the only kind in v0.2; dockerfile (synthesized
// compose) lands in Phase 3. The process kind from DESIGN §5.5 was
// dropped — pier is intentionally docker-coupled, even for raw-process
// stacks (uv/npm/cargo), because it keeps a single execution path,
// avoids host port/PID/log management, and works on any platform docker
// supports. See README's "minimal compose" example.
package adapter

import (
	"errors"
	"fmt"
	"io"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Ctx carries everything an adapter needs to run a command.
type Ctx struct {
	Project        string         // manifest.project.name
	Slug           string         // derived or overridden DNS slug
	BaseDomain     string         // manifest.project.base_domain
	WorktreePath   string         // git toplevel
	Stack          manifest.Stack // adapter-specific fields under manifest.stack
	TraefikNetwork string         // docker network the workload joins for label discovery
	Out            io.Writer      // command output sink
	Err            io.Writer      // command error sink
}

// Handle is the state to persist after a successful Up.
type Handle struct {
	ContainerID string // compose / dockerfile
}

// Adapter is implemented per stack kind.
type Adapter interface {
	Up(c Ctx) (*Handle, error)
	Down(c Ctx) error
	Logs(c Ctx, follow bool, tail int) error
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

// Name is the canonical container/router/project identifier.
func Name(project, slug string) string { return project + "-" + slug }

// URL is the user-visible URL for the workload.
func URL(slug, baseDomain string) string { return "http://" + slug + "." + baseDomain }

// RecordName is the DNS name written to headscale's extra_records JSON
// — same shape as URL, minus the scheme.
func RecordName(slug, baseDomain string) string { return slug + "." + baseDomain }
