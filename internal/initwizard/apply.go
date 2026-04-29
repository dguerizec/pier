package initwizard

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
)

func ensureEnv(m *manifest.Manifest, service string) {
	if m.Env == nil {
		m.Env = map[string]map[string]string{}
	}
	if m.Env[service] == nil {
		m.Env[service] = map[string]string{}
	}
}

// Apply validates the Plan and writes .pier.toml + .gitignore entries.
//
// On re-init (Plan.Existing != nil) the existing manifest is used as the
// base so user-curated sections — env, materialize, hooks, watch and
// stack.match_host_uid — pass through untouched. Apply only rewrites the
// sections the wizard owns: project, expose, worktree and the wizard
// fields of stack (kind, file, service).
//
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

	if p.IsReinit() {
		if dropped := droppedServices(p.Existing.Expose, exposes); len(dropped) > 0 {
			fmt.Fprintf(stdout, "note: dropping previously exposed services: %v\n", dropped)
		}
	}

	m := p.Existing
	if m == nil {
		m = &manifest.Manifest{}
	}
	m.Project = manifest.Project{Name: p.Name, BaseDomain: p.Domain}
	// Preserve stack.match_host_uid; only rewrite the wizard-owned fields.
	m.Stack.Kind = manifest.KindCompose
	m.Stack.File = relTo(p.Toplevel, p.ComposeFile)
	m.Stack.Dockerfile = "" // wizard only emits compose stacks
	m.Stack.Service = p.DefaultService
	m.Expose = exposes
	// worktree.dir is a per-user preference, not a project setting:
	// fresh manifests omit it entirely, so the resolution chain
	// (.pier.local.toml → .pier.toml → prefs.toml → default) lands on
	// the user's prefs at lookup time. Re-init preserves an explicit
	// project pin if one is already in the manifest.
	m.Worktree.BaseRef = p.BaseRef
	if !p.IsReinit() || p.Existing.Worktree.Dir == "" {
		m.Worktree.Dir = ""
	}

	if p.WorktreeDirExplicit {
		if err := persistWorktreeDirPref(p.WorktreeDir, stdout); err != nil {
			fmt.Fprintf(stdout, "warning: could not save worktree dir to prefs: %v\n", err)
		}
	}

	for _, s := range p.AcceptedEnvSuggestions() {
		ensureEnv(m, s.Service)
		m.Env[s.Service][s.Key] = s.Replacement
	}
	for i, prompt := range p.EnvVarPrompts {
		if i >= len(p.EnvVarValues) {
			break
		}
		val := strings.TrimSpace(p.EnvVarValues[i])
		if val == "" {
			continue
		}
		ensureEnv(m, prompt.Service)
		m.Env[prompt.Service][prompt.Key] = val
	}

	if err := m.Validate(); err != nil {
		return err
	}
	if err := m.Write(p.ManifestPath); err != nil {
		return err
	}

	// Gitignore management is a first-init concern. On re-init the user's
	// gitignore decisions (committed vs private) are already settled; we
	// don't add or remove entries based on a re-run flag.
	if !p.IsReinit() {
		if !p.Share {
			warnIfErr(stdout, EnsureGitignore(p.Toplevel, manifest.FileName))
		}
		warnIfErr(stdout, EnsureGitignore(p.Toplevel, manifest.LocalFileName))
		warnIfErr(stdout, EnsureGitignore(p.Toplevel, ".pier/"))
		if entry := WorktreeDirGitignoreEntry(p.Toplevel, p.WorktreeDir); entry != "" {
			warnIfErr(stdout, EnsureGitignore(p.Toplevel, entry))
		}
	}

	verb := "written"
	if p.IsReinit() {
		verb = "updated"
	}
	fmt.Fprintf(stdout, "✓ %s %s\n", p.ManifestPath, verb)
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

// droppedServices returns the services that were in the previous manifest
// but no longer appear in the new exposes list — typically because the
// service was renamed or removed in the compose file.
func droppedServices(prev, next []manifest.ExposeRule) []string {
	keep := map[string]bool{}
	for _, e := range next {
		keep[e.Service] = true
	}
	var out []string
	for _, e := range prev {
		if !keep[e.Service] {
			out = append(out, e.Service)
		}
	}
	return out
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

// persistWorktreeDirPref saves dir into ~/.config/pier/prefs.toml,
// preserving any other prefs already on disk. Called only when the user
// passed --worktree-dir explicitly.
func persistWorktreeDirPref(dir string, stdout io.Writer) error {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return err
	}
	prefs, err := infra.LoadPrefs(paths)
	if err != nil {
		return err
	}
	if prefs.WorktreeDir == dir {
		return nil
	}
	prefs.WorktreeDir = dir
	if err := prefs.Save(paths); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "✓ saved worktree dir to %s\n", infra.PrefsPath(paths))
	return nil
}
