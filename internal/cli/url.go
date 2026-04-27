package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/adapter"
)

func newURLCmd() *cobra.Command {
	var slug string
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the URL of the current worktree's workload",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDaily(slug)
			if err != nil {
				return err
			}
			defer d.State.Close()
			fmt.Fprintln(cmd.OutOrStdout(), adapter.URL(d.Ctx.Slug, d.Ctx.BaseDomain))
			return nil
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "override derived slug")
	return cmd
}
