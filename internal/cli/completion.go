package cli

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/manifest"
	sluglib "github.com/dguerizec/pier/internal/slug"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/worktree"
)

// slugCompletion suggests values for --slug. It mirrors what
// resolveSlugInput will accept: canonical slugs, local branches, and
// worktree basenames — but only when the current repo carries a pier
// manifest, since resolveDaily needs one to resolve --slug. State-DB slugs
// belonging to the current project are added too.
//
// Outside a pier-managed repo, --slug has no meaningful target, so we
// return nothing rather than suggest names that would fail at runtime.
// Use --project for cross-repo inspection.
func slugCompletion(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	info, err := worktree.Detect()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	m, err := manifest.Load(info.Toplevel)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	if entries, err := worktree.List(info.Toplevel); err == nil {
		for _, e := range entries {
			if e.Branch == "" {
				continue
			}
			add(e.Branch)
			add(filepath.Base(e.Path))
			if s, err := sluglib.FromBranch(e.Branch); err == nil {
				add(s)
			}
		}
	}

	if paths, err := infra.DefaultPaths(); err == nil {
		if store, err := state.Open(paths.StateDB); err == nil {
			defer store.Close()
			if loads, err := store.List(); err == nil {
				for _, w := range loads {
					if w.Project == m.Project.Name {
						add(w.Slug)
					}
				}
			}
		}
	}

	return out, cobra.ShellCompDirectiveNoFileComp
}

// psSlugCompletion is slugCompletion's polymorphic cousin used by `pier ps`.
// With a manifest in cwd it behaves like slugCompletion (bare slugs).
// Without one, it falls back to `<project>-<slug>` strings from the state
// DB so users can target any running workload from anywhere — ps's
// resolveProjectName accepts that form too.
func psSlugCompletion(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	if info, err := worktree.Detect(); err == nil {
		if _, err := manifest.Load(info.Toplevel); err == nil {
			return slugCompletion(nil, nil, "")
		}
	}
	return projectCompletion(nil, nil, "")
}

// projectCompletion suggests values for --project (the docker compose
// project name, i.e. <project>-<slug>). Pulled from the state DB so it
// always matches what's actually running. Works from anywhere — the whole
// point of --project is cross-repo inspection.
func projectCompletion(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer store.Close()
	loads, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(loads))
	for _, w := range loads {
		out = append(out, w.Project+"-"+w.Slug)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// serviceCompletion suggests compose service names parsed from the
// manifest's stack.file. Used by `pier logs [SERVICE...]` so the user
// can tab-complete service names instead of memorising the compose file.
// Already-typed services are filtered out so repeated tab cycles only
// offer the remaining ones. Returns nothing outside a pier-managed repo
// or when the compose file can't be parsed — the positional is still
// accepted at runtime, completion just stays quiet.
func serviceCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	info, err := worktree.Detect()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	m, err := manifest.Load(info.Toplevel)
	if err != nil || m.Stack.File == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	stackPath := m.Stack.File
	if !filepath.IsAbs(stackPath) {
		stackPath = filepath.Join(info.Toplevel, stackPath)
	}
	already := map[string]bool{}
	for _, a := range args {
		already[a] = true
	}
	var out []string
	for _, name := range adapter.ListComposeServices(stackPath) {
		if !already[name] {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// registerSlugCompletion wires slugCompletion onto cmd's --slug flag.
// Must be called after the flag is registered.
func registerSlugCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("slug", slugCompletion)
}

// registerProjectCompletion wires projectCompletion onto cmd's --project flag.
func registerProjectCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("project", projectCompletion)
}
