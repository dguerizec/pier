package cli

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
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
			project, err := resolveProjectName(opts)
			if err != nil {
				return err
			}
			full := append([]string{"compose", "-p", project, "ps"}, args...)
			c := exec.Command("docker", full...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Stdin = os.Stdin
			return c.Run()
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.StringVarP(&opts.project, "project", "p", "", "full compose project name (skips manifest)")
	return cmd
}

// resolveProjectName returns the docker compose project name (-p) for ps.
// --project shortcuts manifest loading entirely; otherwise we go through
// resolveDaily so the slug comes from branch / PIER_SLUG / --slug like the
// other daily commands.
func resolveProjectName(opts psOpts) (string, error) {
	if opts.project != "" {
		return opts.project, nil
	}
	d, err := resolveDaily(opts.slug)
	if err != nil {
		return "", err
	}
	defer d.State.Close()
	return adapter.Name(d.Manifest.Project.Name, d.Slug), nil
}
