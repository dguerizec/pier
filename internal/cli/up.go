package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
	"github.com/LeoPartt/pier/internal/headscale"
	"github.com/LeoPartt/pier/internal/materialize"
	"github.com/LeoPartt/pier/internal/state"
)

type upOpts struct {
	slug             string
	fresh            bool
	ignoreHookErrors bool
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
			return runUp(d, opts.ignoreHookErrors, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.fresh, "fresh", false, "skip snapshot copy, mkdir empty dirs instead (post-MVP)")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "continue when a [hooks].pre_up / post_up command fails")
	registerSlugCompletion(cmd)
	return cmd
}

// runUp materializes files, calls the adapter's Up, persists the workload
// in state, registers headscale records when configured, and prints URLs.
// Shared between the cobra command and the REST POST /up handler — keep
// it pure so the API can call it with io.Discard writers without
// surprising the CLI flow.
func runUp(d *daily, ignoreHookErrors bool, out, errOut io.Writer) error {
	hc := buildHookContext(d.Worktree.PrimaryPath, d.Worktree.Toplevel, d.Worktree.Branch, d.Manifest, errOut)
	if err := materialize.RunHooks("pre_up", d.Manifest.Hooks.PreUp, hc, out, errOut); err != nil {
		if ignoreHookErrors {
			fmt.Fprintf(errOut, "! pre_up failed (continuing because --ignore-hook-errors): %v\n", err)
		} else {
			return fmt.Errorf("pre_up hook: %w (use --ignore-hook-errors to start anyway)", err)
		}
	}

	if err := materialize.Apply(d.Worktree.PrimaryPath, d.Worktree.Toplevel, d.Manifest.Materialize, out); err != nil {
		return err
	}

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
	})
	if err != nil {
		return fmt.Errorf("persist workload: %w", err)
	}

	if d.Config.HeadscaleRecordsPath != "" {
		ip := d.Config.EffectiveAnswerIP()
		for _, name := range adapter.RecordNames(d.Ctx) {
			added, err := headscale.Add(d.Config.HeadscaleRecordsPath, name, ip)
			if errors.Is(err, headscale.ErrConflict) {
				fmt.Fprintf(errOut, "! headscale: %s already mapped elsewhere; not overwriting\n", name)
			} else if err != nil {
				return fmt.Errorf("headscale records: %w", err)
			} else if added {
				fmt.Fprintf(out, "✓ headscale record: %s → %s\n", name, ip)
			}
		}
	}

	for _, u := range adapter.URLs(d.Ctx) {
		fmt.Fprintf(out, "→ %s\n", u)
	}

	if err := materialize.RunHooks("post_up", d.Manifest.Hooks.PostUp, hc, out, errOut); err != nil {
		if ignoreHookErrors {
			fmt.Fprintf(errOut, "! post_up failed (continuing because --ignore-hook-errors): %v\n", err)
		} else {
			return fmt.Errorf("post_up hook: %w (workload is up; use --ignore-hook-errors to silence)", err)
		}
	}
	return nil
}
