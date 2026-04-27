package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

type logsOpts struct {
	follow bool
	tail   int
}

func newLogsCmd() *cobra.Command {
	var opts logsOpts
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail container/process logs for the current worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("logs: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.follow, "follow", "f", false, "follow log output")
	f.IntVar(&opts.tail, "tail", 200, "number of lines to show from the end")
	return cmd
}
