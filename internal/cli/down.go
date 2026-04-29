package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/headscale"
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
			return runDown(d, opts.purge, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.purge, "purge", false, "also wipe materialized snapshots")
	registerSlugCompletion(cmd)
	return cmd
}

// runDown stops the workload via the adapter, drops the state row,
// removes headscale records when configured, and (optionally) purges
// materialized snapshots. Shared with the REST POST /down handler.
func runDown(d *daily, purge bool, out, errOut io.Writer) error {
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

	if d.Config.HeadscaleRecordsPath != "" {
		for _, name := range adapter.RecordNames(d.Ctx) {
			if removed, err := headscale.Remove(d.Config.HeadscaleRecordsPath, name); err != nil {
				fmt.Fprintf(errOut, "! headscale records remove %s: %v\n", name, err)
			} else if removed {
				fmt.Fprintf(out, "✓ headscale record removed: %s\n", name)
			}
		}
	}

	if purge {
		if err := materialize.Purge(d.Worktree.Toplevel, d.Manifest.Materialize, out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "✓ %s stopped\n", adapter.Name(d.Ctx.Project, d.Ctx.Slug))
	return nil
}
