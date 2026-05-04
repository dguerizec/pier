package materialize

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// HookContext is the per-worktree context exposed to post_create /
// pre_remove scripts as PIER_* env vars. All fields are populated by
// the caller (CLI worktree command) so this package stays free of
// manifest/slug imports beyond what Apply already needs.
type HookContext struct {
	WorktreePath string // absolute; PIER_WORKTREE_PATH
	PrimaryPath  string // absolute; PIER_PRIMARY_PATH
	Slug         string // PIER_SLUG (DNS label derived from branch)
	Branch       string // PIER_BRANCH (raw branch name)
	BaseDomain   string // PIER_BASE_DOMAIN (post-template, may be empty)
	ProjectName  string // PIER_PROJECT_NAME
}

// Env returns the PIER_* env vars layered on top of os.Environ().
// Empty fields are still emitted so scripts can rely on the keys
// existing (unset vs "" is a footgun for `set -u` users).
func (h HookContext) Env() []string {
	return append(os.Environ(),
		"PIER_WORKTREE_PATH="+h.WorktreePath,
		"PIER_PRIMARY_PATH="+h.PrimaryPath,
		"PIER_SLUG="+h.Slug,
		"PIER_BRANCH="+h.Branch,
		"PIER_BASE_DOMAIN="+h.BaseDomain,
		"PIER_PROJECT_NAME="+h.ProjectName,
	)
}

// RunHooks executes cmds in order via `sh -c`, with cwd set to
// hc.WorktreePath. The first non-zero exit aborts the sequence and
// returns the error; the caller decides whether to roll back.
//
// Stdout/stderr are streamed to the provided writers (no capture) so
// the user sees progress live. label is "post_create" / "pre_remove" —
// used in error messages and progress prints.
func RunHooks(label string, cmds []string, hc HookContext, out, errOut io.Writer) error {
	if len(cmds) == 0 {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	env := hc.Env()
	for i, raw := range cmds {
		fmt.Fprintf(out, "▸ %s[%d]: %s\n", label, i, raw)
		c := exec.Command("sh", "-c", raw)
		c.Dir = hc.WorktreePath
		c.Env = env
		c.Stdout = out
		c.Stderr = errOut
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s[%d] %q: %w", label, i, raw, err)
		}
	}
	return nil
}
