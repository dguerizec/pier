package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "pier",
	Short: "Stable URLs for every git worktree on a local dev domain",
	Long: `pier gives every git worktree a stable URL on a local dev domain
with zero per-project DNS or proxy plumbing.

Bootstrap the shared infra layer once with 'pier install', then use
'pier init' per repo and 'pier up' / 'pier down' per worktree.`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(
		newInstallCmd(),
		newUninstallCmd(),
		newInitCmd(),
		newUpCmd(),
		newDownCmd(),
		newURLCmd(),
		newLsCmd(),
		newLogsCmd(),
		newWatchCmd(),
		newGCCmd(),
		newClientCmd(),
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
