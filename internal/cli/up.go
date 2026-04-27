package cli

import (
	"errors"

	"github.com/spf13/cobra"
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
			return errors.New("up: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	f.BoolVar(&opts.fresh, "fresh", false, "skip snapshot copy, mkdir empty dirs instead")
	return cmd
}
