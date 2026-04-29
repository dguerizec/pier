package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/initwizard"
	"github.com/LeoPartt/pier/internal/worktree"
)

type initOpts struct {
	name        string
	domain      string
	service     string
	file        string
	private     bool
	yes         bool
	worktreeDir string
	baseRef     string
}

func (o initOpts) toWizard() initwizard.Opts {
	return initwizard.Opts{
		Name:        o.name,
		Domain:      o.domain,
		Service:     o.service,
		File:        o.file,
		Private:     o.private,
		Yes:         o.yes,
		WorktreeDir: o.worktreeDir,
		BaseRef:     o.baseRef,
	}
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

	return initwizard.Apply(plan, stdout)
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
