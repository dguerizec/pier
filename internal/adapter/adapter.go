// Package adapter abstracts how a workload is brought up, taken down, and
// inspected. compose, process, and dockerfile each implement the same
// interface so daily commands stay agnostic.
package adapter

import (
	"errors"
	"fmt"
	"io"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Ctx carries everything an adapter needs to run a command.
type Ctx struct {
	Project      string          // manifest.project.name
	Slug         string          // derived or overridden DNS slug
	BaseDomain   string          // manifest.project.base_domain
	WorktreePath string          // git toplevel
	Stack        manifest.Stack  // adapter-specific fields under manifest.stack
	Out          io.Writer       // command output sink
	Err          io.Writer       // command error sink
}

// Handle is the state to persist after a successful Up.
type Handle struct {
	ContainerID string // compose / dockerfile
	PID         int64  // process kind
	Port        int    // process kind
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
	case manifest.KindProcess, manifest.KindDockerfile:
		return nil, fmt.Errorf("%w: %q (compose-only in MVP)", ErrUnsupportedKind, kind)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
}

// Name is the canonical container/router/project identifier.
func Name(project, slug string) string { return project + "-" + slug }

// URL is the user-visible URL for the workload.
func URL(slug, baseDomain string) string { return "http://" + slug + "." + baseDomain }
