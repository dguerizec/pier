package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LeoPartt/pier/internal/systemd"
)

func newServeInstallCmd() *cobra.Command {
	var (
		scopeFlag string
		printOnly bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pier serve as a systemd unit",
		Long: `Writes a pier.service unit and activates it via systemctl.

For --user, drives 'systemctl --user daemon-reload && enable --now'
directly. For --system, stages the unit in /tmp and shells out to sudo
for the install/reload/enable steps (sudo prompts for the password).

Pass --print-only to skip exec and print the commands instead — useful
in CI, scripted bootstraps, or when you route privilege escalation
through your own tooling.

Without --user/--system the scope auto-detects: root → system, otherwise user.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := systemd.ParseScope(scopeFlag)
			if err != nil {
				return err
			}
			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate self: %w", err)
			}
			_, err = systemd.Install(scope, bin, printOnly, cmd.OutOrStdout())
			return err
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user|system (default: auto-detect from euid)")
	cmd.Flags().BoolVar(new(bool), "user", false, "shorthand for --scope=user")
	cmd.Flags().BoolVar(new(bool), "system", false, "shorthand for --scope=system")
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl/sudo commands instead of running them")
	cmd.PreRunE = func(c *cobra.Command, _ []string) error {
		return resolveScopeShorthand(c, &scopeFlag)
	}
	return cmd
}

func newServeUninstallCmd() *cobra.Command {
	var (
		scopeFlag string
		printOnly bool
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pier serve systemd unit",
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := systemd.ParseScope(scopeFlag)
			if err != nil {
				return err
			}
			return systemd.Uninstall(scope, printOnly, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user|system (default: auto-detect from euid)")
	cmd.Flags().BoolVar(new(bool), "user", false, "shorthand for --scope=user")
	cmd.Flags().BoolVar(new(bool), "system", false, "shorthand for --scope=system")
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "print the systemctl/sudo commands instead of running them")
	cmd.PreRunE = func(c *cobra.Command, _ []string) error {
		return resolveScopeShorthand(c, &scopeFlag)
	}
	return cmd
}

// resolveScopeShorthand maps the convenience --user/--system bool flags
// onto the underlying --scope string. Done in PreRunE so RunE only has
// to read one flag and so we can reject the obviously-wrong "--user
// --system" combination once.
func resolveScopeShorthand(c *cobra.Command, scope *string) error {
	user, _ := c.Flags().GetBool("user")
	system, _ := c.Flags().GetBool("system")
	if user && system {
		return fmt.Errorf("--user and --system are mutually exclusive")
	}
	if (user || system) && *scope != "" {
		return fmt.Errorf("--scope is incompatible with --user/--system shorthand")
	}
	switch {
	case user:
		*scope = "user"
	case system:
		*scope = "system"
	}
	return nil
}
