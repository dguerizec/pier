package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/systemd"
)

func newServeInstallCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pier serve as a systemd --user unit",
		Long: `Writes a pier.service unit under ~/.config/systemd/user/ and runs
'systemctl --user daemon-reload && enable --now'.

Pass --print-only to skip exec and print the commands instead — useful
in CI, scripted bootstraps, or when you route privilege escalation
through your own tooling.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate self: %w", err)
			}
			_, err = systemd.Install(bin, printOnly, cmd.OutOrStdout())
			return err
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl commands instead of running them")
	return cmd
}

func newServeUninstallCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pier serve systemd --user unit",
		RunE: func(cmd *cobra.Command, args []string) error {
			return systemd.Uninstall(printOnly, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl commands instead of running them")
	return cmd
}
