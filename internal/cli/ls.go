package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

type lsOpts struct {
	json bool
}

func newLsCmd() *cobra.Command {
	var opts lsOpts
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List active workloads across all projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("ls: not implemented yet")
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "machine-readable JSON output")
	return cmd
}
