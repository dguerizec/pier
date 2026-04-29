package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Foreground: re-up on file changes (opt-in)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("watch: not implemented yet")
		},
	}
}
