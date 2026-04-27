package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/state"
)

type upOpts struct {
	slug  string
	fresh bool
}

func newUpCmd() *cobra.Command {
	var opts upOpts
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Materialize files and start the workload for the current worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDaily(opts.slug)
			if err != nil {
				return err
			}
			defer d.State.Close()

			a, err := adapter.For(d.Manifest.Stack.Kind)
			if err != nil {
				return err
			}

			h, err := a.Up(d.Ctx)
			if err != nil {
				return err
			}

			err = d.State.Upsert(&state.Workload{
				Project:      d.Ctx.Project,
				Slug:         d.Ctx.Slug,
				WorktreePath: d.Ctx.WorktreePath,
				Branch:       d.Worktree.Branch,
				Kind:         d.Manifest.Stack.Kind,
				ContainerID:  h.ContainerID,
				PID:          h.PID,
				Port:         h.Port,
			})
			if err != nil {
				return fmt.Errorf("persist workload: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "→ %s\n", adapter.URL(d.Ctx.Slug, d.Ctx.BaseDomain))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.fresh, "fresh", false, "skip snapshot copy, mkdir empty dirs instead (post-MVP)")
	return cmd
}
