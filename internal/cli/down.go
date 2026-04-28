package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/materialize"
)

type downOpts struct {
	slug  string
	purge bool
}

func newDownCmd() *cobra.Command {
	var opts downOpts
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop workload, free the slot, keep data by default",
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
			if err := a.Down(d.Ctx); err != nil {
				return err
			}

			if err := d.State.Delete(d.Ctx.Project, d.Ctx.Slug); err != nil {
				return fmt.Errorf("delete state row: %w", err)
			}

			if opts.purge {
				if err := materialize.Purge(d.Worktree.Toplevel, d.Manifest.Materialize, cmd.OutOrStdout()); err != nil {
					return err
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s stopped\n", adapter.Name(d.Ctx.Project, d.Ctx.Slug))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.purge, "purge", false, "also wipe materialized snapshots")
	return cmd
}
