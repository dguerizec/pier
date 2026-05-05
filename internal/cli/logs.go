package cli

import (
	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
)

type logsOpts struct {
	follow bool
	tail   int
	slug   string
}

func newLogsCmd() *cobra.Command {
	var opts logsOpts
	cmd := &cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: "Tail container/process logs for the current worktree",
		Long:  "Tail logs for the current worktree. With no SERVICE argument every compose service streams (multi-expose default). Pass one or more service names to restrict the stream, same semantics as `docker compose logs [SERVICE...]`.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: serviceCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDaily(cmd, opts.slug)
			if err != nil {
				return err
			}
			defer d.State.Close()
			a, err := adapter.For(d.Manifest.Stack.Kind)
			if err != nil {
				return err
			}
			return a.Logs(d.Ctx, opts.follow, opts.tail, args)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.follow, "follow", "f", false, "follow log output")
	f.IntVar(&opts.tail, "tail", 200, "number of lines to show from the end")
	f.StringVar(&opts.slug, "slug", "", "override derived slug")
	registerSlugCompletion(cmd)
	return cmd
}
