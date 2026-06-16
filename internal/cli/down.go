package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/materialize"
)

type downOpts struct {
	slug             string
	purge            bool
	ignoreHookErrors bool
}

func newDownCmd() *cobra.Command {
	var opts downOpts
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop workload, free the slot, keep data by default",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDaily(cmd, opts.slug)
			if err != nil {
				return err
			}
			defer d.State.Close()
			return runDown(d, opts.purge, opts.ignoreHookErrors, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.purge, "purge", false, "also wipe materialized snapshots")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "continue when a [hooks].pre_down / post_down command fails")
	registerSlugCompletion(cmd)
	return cmd
}

// runDown stops the workload via the adapter, drops the state row,
// removes headscale records when configured, and (optionally) purges
// materialized snapshots. Shared with the REST POST /down handler.
func runDown(d *daily, purge, ignoreHookErrors bool, out, errOut io.Writer) error {
	hc := buildHookContext(d.Worktree.PrimaryPath, d.Worktree.Toplevel, d.Worktree.Branch, d.Manifest, errOut)
	if err := materialize.RunHooks("pre_down", d.Manifest.Hooks.PreDown, hc, out, errOut); err != nil {
		if ignoreHookErrors {
			fmt.Fprintf(errOut, "! pre_down failed (continuing because --ignore-hook-errors): %v\n", err)
		} else {
			return fmt.Errorf("pre_down hook: %w (use --ignore-hook-errors to stop anyway)", err)
		}
	}

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

	if purge {
		if err := materialize.Purge(d.Worktree.Toplevel, d.Manifest.Materialize, out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "✓ %s stopped\n", adapter.Name(d.Ctx.Project, d.Ctx.Slug))

	if err := materialize.RunHooks("post_down", d.Manifest.Hooks.PostDown, hc, out, errOut); err != nil {
		if ignoreHookErrors {
			fmt.Fprintf(errOut, "! post_down failed (continuing because --ignore-hook-errors): %v\n", err)
		} else {
			return fmt.Errorf("post_down hook: %w (workload is down; use --ignore-hook-errors to silence)", err)
		}
	}
	return nil
}
