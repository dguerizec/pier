package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

type gcOpts struct {
	yes           bool
	removeWorktree bool
}

func newGCCmd() *cobra.Command {
	var opts gcOpts
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove orphaned workloads (worktree gone or branch deleted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("gc: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.yes, "yes", "y", false, "do not prompt before removal")
	f.BoolVar(&opts.removeWorktree, "remove-worktree", false, "also run git worktree remove")
	return cmd
}
