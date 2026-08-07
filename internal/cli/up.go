package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/adapter"
	"github.com/dguerizec/pier/internal/applied"
	"github.com/dguerizec/pier/internal/materialize"
	"github.com/dguerizec/pier/internal/state"
)

type upOpts struct {
	slug             string
	fresh            bool
	ignoreHookErrors bool
	waitTimeout      time.Duration
}

func newUpCmd() *cobra.Command {
	var opts upOpts
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Build and reconcile the workload for the current worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.waitTimeout <= 0 {
				return errors.New("--wait-timeout must be greater than zero")
			}
			d, err := resolveDailyFresh(cmd, opts.slug)
			if err != nil {
				return err
			}
			defer d.State.Close()
			d.Ctx.WaitTimeout = opts.waitTimeout
			return runUp(d, opts.ignoreHookErrors, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.fresh, "fresh", false, "skip snapshot copy, mkdir empty dirs instead (post-MVP)")
	f.BoolVar(&opts.ignoreHookErrors, "ignore-hook-errors", false, "continue when a [hooks].pre_up / post_up command fails")
	f.DurationVar(&opts.waitTimeout, "wait-timeout", 2*time.Minute, "maximum time to wait for services to become running/healthy")
	registerSlugCompletion(cmd)
	return cmd
}

// runUp materializes files, prepares without disrupting the current workload,
// replaces an old identity when needed, applies the desired workload, persists
// its teardown state, and prints URLs.
// Shared between the cobra command and the REST POST /up handler — keep
// it pure so the API can call it with io.Discard writers without
// surprising the CLI flow.
func runUp(d *daily, ignoreHookErrors bool, out, errOut io.Writer) error {
	hc := buildHookContext(d.Worktree.PrimaryPath, d.Worktree.Toplevel, d.Worktree.Branch, d.Manifest, errOut)
	hc.Slug = d.Slug
	hc.RuntimeEnv = d.Ctx.ComposeEnv
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

	previous, err := previousAppliedForUp(d.Worktree.Toplevel, d.Slug)
	if err != nil {
		return err
	}
	prepared, err := a.Prepare(d.Ctx)
	if err != nil {
		return err
	}
	if previous != nil && !previous.SameIdentity(d.Ctx) {
		fmt.Fprintf(out, "▸ replacing applied workload %s with %s\n",
			adapter.Name(previous.Project, previous.Slug), adapter.Name(d.Ctx.Project, d.Ctx.Slug))
		old := dailyFromApplied(d, previous, out, errOut)
		if err := runDown(old, false, ignoreHookErrors, out, errOut); err != nil {
			return fmt.Errorf("stop previous applied workload: %w", err)
		}
	}

	h, applyErr := a.Apply(d.Ctx, prepared)
	if h != nil {
		if err := persistAppliedWorkload(d, prepared, h, errOut); err != nil {
			if applyErr != nil {
				return errors.Join(applyErr, err)
			}
			return err
		}
	}
	if applyErr != nil {
		return applyErr
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

func previousAppliedForUp(worktreePath, desiredSlug string) (*applied.State, error) {
	state, err := applied.Load(worktreePath, desiredSlug)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, applied.ErrNotFound) {
		return nil, err
	}
	states, err := applied.List(worktreePath)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, nil
	}
	if len(states) == 1 {
		return states[0], nil
	}
	return nil, fmt.Errorf("multiple applied workloads exist in %s; stop the old workload with pier down --slug before changing slug", worktreePath)
}

func persistAppliedWorkload(d *daily, prepared *adapter.Prepared, h *adapter.Handle, errOut io.Writer) error {
	appliedState := applied.New(
		d.Ctx,
		d.Worktree.PrimaryPath,
		d.Worktree.Branch,
		d.Manifest,
		prepared,
		h,
	)
	if err := applied.Save(appliedState); err != nil {
		return fmt.Errorf("persist applied workload (workload is running): %w", err)
	}
	registerProjectForUp(d, errOut)

	if err := d.State.Upsert(&state.Workload{
		Project:      d.Ctx.Project,
		Slug:         d.Ctx.Slug,
		WorktreePath: d.Ctx.WorktreePath,
		Branch:       d.Worktree.Branch,
		Kind:         d.Manifest.Stack.Kind,
		ContainerID:  h.ContainerID,
	}); err != nil {
		return fmt.Errorf("persist workload: %w", err)
	}
	return nil
}

func dailyFromApplied(current *daily, state *applied.State, out, errOut io.Writer) *daily {
	ctx := state.Context(out, errOut)
	ctx.WorktreePath = current.Worktree.Toplevel
	return &daily{
		Worktree: current.Worktree,
		Manifest: &state.Manifest,
		Slug:     state.Slug,
		Ctx:      ctx,
		State:    current.State,
		Paths:    current.Paths,
		Config:   current.Config,
		Applied:  state,
	}
}

// registerProjectForUp makes an existing .pier.toml sufficient for the
// dashboard/API project surface. The primary worktree is the stable repo
// identity; registering a secondary path would create one registry row per
// branch. Registry metadata must never stop the workload lifecycle, so a
// genuine name/path conflict is surfaced as a warning and up continues.
func registerProjectForUp(d *daily, errOut io.Writer) {
	repoPath := d.Worktree.PrimaryPath
	if repoPath == "" {
		repoPath = d.Worktree.Toplevel
	}
	if _, err := d.State.RegisterOrRenameProject(d.Manifest.Project.Name, repoPath); err != nil {
		fmt.Fprintf(errOut, "! registry: %v\n", err)
	}
}
