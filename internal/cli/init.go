package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/initwizard"
	"github.com/dguerizec/pier/internal/state"
	"github.com/dguerizec/pier/internal/worktree"
)

type initOpts struct {
	name                 string
	domain               string
	service              string
	file                 string
	private              bool
	yes                  bool
	worktreeDir          string
	baseRef              string
	matchHostUID         bool
	matchHostUIDExplicit bool
}

func (o initOpts) toWizard() initwizard.Opts {
	w := initwizard.Opts{
		Name:        o.name,
		Domain:      o.domain,
		Service:     o.service,
		File:        o.file,
		Private:     o.private,
		Yes:         o.yes,
		WorktreeDir: o.worktreeDir,
		BaseRef:     o.baseRef,
	}
	if o.matchHostUIDExplicit {
		v := o.matchHostUID
		w.MatchHostUID = &v
	}
	return w
}

func newInitCmd() *cobra.Command {
	var opts initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect project type, generate .pier.toml manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := worktree.Detect()
			if err != nil {
				return err
			}
			if !info.IsPrimary {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: running pier init on a secondary worktree; the manifest will live there only")
			}
			// cobra has no first-class --no-X negation, so we read the
			// Changed() bit on a single flag whose value can be set
			// either way (`--match-host-uid=false` or `--no-match-host-uid`
			// alias). Without an explicit flag the wizard prompts.
			opts.matchHostUIDExplicit = cmd.Flags().Changed("match-host-uid") ||
				cmd.Flags().Changed("no-match-host-uid")
			if cmd.Flags().Changed("no-match-host-uid") {
				opts.matchHostUID = false
			}
			return runInit(cmd.OutOrStdout(), info.Toplevel, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.name, "name", "", "project name (default: directory name)")
	f.StringVar(&opts.domain, "domain", "", "base domain (default: <name>.{pier.tld})")
	f.StringVar(&opts.service, "service", "", "service that gets the bare <slug>.<base_domain> alias (default: first exposed)")
	f.StringVar(&opts.file, "file", "", "compose file path (default: auto-detect)")
	f.BoolVar(&opts.private, "private", false, "gitignore .pier.toml (default: commit it so secondary worktrees inherit it)")
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept all defaults, no prompts")
	f.StringVar(&opts.worktreeDir, "worktree-dir", "", "where `pier worktree add <name>` places trees (default: .pier/worktrees)")
	f.StringVar(&opts.baseRef, "base-ref", "", "ref new worktree branches fork from (default: detected main/master)")
	f.BoolVar(&opts.matchHostUID, "match-host-uid", true, "run containers as your host UID:GID (avoids root-owned files in bind mounts; safe default for distroless/nonroot images)")
	// Boolean flags don't auto-generate --no-X in cobra/pflag, so we
	// register an explicit alias. The handler distinguishes the two via
	// Flags().Changed() to populate the wizard's tri-state Opts.
	f.Bool("no-match-host-uid", false, "shorthand for --match-host-uid=false")
	return cmd
}

func runInit(stdout io.Writer, toplevel string, opts initOpts) error {
	plan, ambig, err := initwizard.Derive(toplevel, opts.toWizard())
	if err != nil {
		return err
	}

	if plan.IsReinit() {
		fmt.Fprintln(stdout, "Updating existing .pier.toml; user-curated sections (env, materialize, hooks, watch) preserved.")
	}
	printDetection(stdout, plan)

	if !opts.yes && len(ambig) > 0 && initwizard.IsInteractive() {
		if err := initwizard.PromptHuh(plan, ambig); err != nil {
			return err
		}
	}

	if err := initwizard.Apply(plan, stdout); err != nil {
		return err
	}
	registerProjectAfterInit(stdout, plan.Name, plan.Toplevel)
	return nil
}

// registerProjectAfterInit records the (project name, repo path) pair in
// the state DB so the API surface and other tooling can look up the repo
// from the project name without the user having to remember it. Best
// effort: registry failures are logged but do not bubble up — the
// manifest is already written, that's the user-visible win.
func registerProjectAfterInit(stdout io.Writer, name, toplevel string) {
	paths, err := infra.DefaultPaths()
	if err != nil {
		fmt.Fprintf(stdout, "! registry: skipped (%v)\n", err)
		return
	}
	store, err := state.Open(paths.StateDB)
	if err != nil {
		fmt.Fprintf(stdout, "! registry: open %s: %v\n", paths.StateDB, err)
		return
	}
	defer store.Close()
	if _, err := store.RegisterProject(name, toplevel); err != nil {
		if errors.Is(err, state.ErrProjectExists) {
			// Conflict on either name or repo with a DIFFERENT mapping —
			// surface so the user can resolve manually. We don't auto-
			// overwrite: a stale registry entry might come from a moved
			// or renamed project.
			fmt.Fprintf(stdout, "! registry: %v\n", err)
			return
		}
		fmt.Fprintf(stdout, "! registry: %v\n", err)
		return
	}
}

func printDetection(stdout io.Writer, plan *initwizard.Plan) {
	fmt.Fprintf(stdout, "Detected: %s\n", filepath.Base(plan.ComposeFile))
	switch len(plan.Candidates) {
	case 1:
		c := plan.Candidates[0]
		fmt.Fprintf(stdout, "  service: %s (container port %d)\n", c.Service, c.Port)
	default:
		fmt.Fprintln(stdout, "  services with published ports:")
		for _, c := range plan.Candidates {
			fmt.Fprintf(stdout, "    - %s (port %d)\n", c.Service, c.Port)
		}
	}
}
