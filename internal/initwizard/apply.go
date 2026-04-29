package initwizard

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Apply validates the Plan and writes .pier.toml + .gitignore entries.
// Status messages are printed to stdout so the CLI doesn't have to know
// about the file layout.
func Apply(p *Plan, stdout io.Writer) error {
	exposes := p.SelectedExposes()
	if len(exposes) == 0 {
		return errors.New("at least one service must be exposed")
	}

	if p.DefaultService != "" && !exposesContain(exposes, p.DefaultService) {
		fmt.Fprintf(stdout, "warning: default service %q is not exposed; no alias will be emitted\n", p.DefaultService)
	}

	m := &manifest.Manifest{
		Project: manifest.Project{Name: p.Name, BaseDomain: p.Domain},
		Stack: manifest.Stack{
			Kind:    manifest.KindCompose,
			File:    relTo(p.Toplevel, p.ComposeFile),
			Service: p.DefaultService,
		},
		Expose: exposes,
		Worktree: manifest.Worktree{
			Dir:     p.WorktreeDir,
			BaseRef: p.BaseRef,
		},
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if err := m.Write(p.ManifestPath); err != nil {
		return err
	}

	if !p.Share {
		warnIfErr(stdout, EnsureGitignore(p.Toplevel, manifest.FileName))
	}
	warnIfErr(stdout, EnsureGitignore(p.Toplevel, manifest.LocalFileName))
	warnIfErr(stdout, EnsureGitignore(p.Toplevel, ".pier/"))
	if entry := WorktreeDirGitignoreEntry(p.Toplevel, p.WorktreeDir); entry != "" {
		warnIfErr(stdout, EnsureGitignore(p.Toplevel, entry))
	}

	fmt.Fprintf(stdout, "✓ %s written\n", p.ManifestPath)
	return nil
}

func exposesContain(rules []manifest.ExposeRule, service string) bool {
	for _, r := range rules {
		if r.Service == service {
			return true
		}
	}
	return false
}

func relTo(base, target string) string {
	if rel, err := filepath.Rel(base, target); err == nil {
		return rel
	}
	return target
}

func warnIfErr(stdout io.Writer, err error) {
	if err != nil {
		fmt.Fprintf(stdout, "warning: could not update .gitignore: %v\n", err)
	}
}
