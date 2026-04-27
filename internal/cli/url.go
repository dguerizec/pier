package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newURLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "url",
		Short: "Print the URL of the current worktree's workload",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("url: not implemented yet")
		},
	}
}
