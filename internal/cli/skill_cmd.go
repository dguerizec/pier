package cli

import "github.com/spf13/cobra"

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage the bundled AI-agent skill",
	}
	cmd.AddCommand(newSkillInstallCmd())
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install or update the bundled AI-agent skill only",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installUserSkill(cmd.InOrStdin(), cmd.OutOrStdout(), yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip conflicting agent-specific skill links without prompting")
	return cmd
}
