package cli

import (
	"errors"

	"github.com/spf13/cobra"
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
			return errors.New("down: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.slug, "slug", "", "override derived slug (use outside a worktree)")
	f.BoolVar(&opts.purge, "purge", false, "also wipe materialized snapshots")
	return cmd
}
