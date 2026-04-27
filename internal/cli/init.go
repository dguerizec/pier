package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

type initOpts struct {
	shared bool
	yes    bool
}

func newInitCmd() *cobra.Command {
	var opts initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect project type, generate .pier.toml manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("init: not implemented yet")
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.shared, "shared", false, "commit manifest to git (post-MVP)")
	f.BoolVarP(&opts.yes, "yes", "y", false, "accept all defaults, no prompts")
	return cmd
}
