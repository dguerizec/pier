package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
)

func newURLCmd() *cobra.Command {
	var (
		slug string
		all  bool
	)
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the URL(s) of the current worktree's workload",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDaily(cmd, slug)
			if err != nil {
				return err
			}
			defer d.State.Close()
			out := cmd.OutOrStdout()
			if all {
				for _, u := range adapter.URLs(d.Ctx) {
					fmt.Fprintln(out, u)
				}
				return nil
			}
			fmt.Fprintln(out, adapter.DefaultURL(d.Ctx))
			return nil
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "override derived slug")
	cmd.Flags().BoolVar(&all, "all", false, "print every URL the workload exposes, not just the default")
	registerSlugCompletion(cmd)
	return cmd
}
