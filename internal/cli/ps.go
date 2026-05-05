package cli

import (
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/state"
)

type psOpts struct {
	slug    string
	project string
}

func newPsCmd() *cobra.Command {
	var opts psOpts
	cmd := &cobra.Command{
		Use:   "ps [-- DOCKER_COMPOSE_PS_ARGS...]",
		Short: "Run `docker compose ps` scoped to the current worktree's project",
		Long: `ps runs ` + "`docker compose -p <project>-<slug> ps`" + ` so you don't have to
remember the namespaced project name.

By default the project and slug come from the current worktree's manifest +
branch. --slug picks a different worktree of the same project; --project
overrides the full ` + "`-p`" + ` value (handy for inspecting another repo without
cd-ing into it).

Pass extra args through after ` + "`--`" + `, e.g. ` + "`pier ps -- -a --format json`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := resolveProjectName(cmd, opts)
			if err != nil {
				return err
			}
			full := append([]string{"compose", "-p", project, "ps"}, args...)
			c := exec.Command("docker", full...)
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			c.Stdin = cmd.InOrStdin()
			return c.Run()
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.StringVarP(&opts.project, "project", "p", "", "full compose project name (skips manifest)")
	_ = cmd.RegisterFlagCompletionFunc("slug", psSlugCompletion)
	registerProjectCompletion(cmd)
	return cmd
}

// resolveProjectName returns the docker compose project name (-p) for ps.
//
// Resolution order:
//  1. --project wins outright.
//  2. resolveDaily — works whenever a manifest is in scope; --slug is
//     interpreted as a slug/branch/worktree relative to that manifest.
//  3. No manifest fallback: --slug treated as a full <project>-<slug>
//     string and looked up in the state DB. Lets `pier ps --slug X` work
//     from anywhere as long as X is currently running.
func resolveProjectName(cmd *cobra.Command, opts psOpts) (string, error) {
	if opts.project != "" {
		return opts.project, nil
	}
	d, err := resolveDaily(cmd, opts.slug)
	if err == nil {
		defer d.State.Close()
		return adapter.Name(d.Manifest.Project.Name, d.Slug), nil
	}
	if opts.slug != "" {
		if name, ok := lookupProjectSlug(opts.slug); ok {
			return name, nil
		}
	}
	return "", err
}

// lookupProjectSlug returns input verbatim when it matches a `<project>-<slug>`
// pair currently recorded in the state DB.
func lookupProjectSlug(input string) (string, bool) {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return "", false
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		return "", false
	}
	defer store.Close()
	loads, err := store.List()
	if err != nil {
		return "", false
	}
	for _, w := range loads {
		if w.Project+"-"+w.Slug == input {
			return input, true
		}
	}
	return "", false
}
