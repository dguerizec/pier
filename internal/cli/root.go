package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Populated at build time via -ldflags by goreleaser. Defaults are used for
// `go install` / `go build` so a self-built binary still reports something.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "pier",
	Short: "Stable URLs for every git worktree on a local dev domain",
	Long: `pier gives every git worktree a stable URL on a local dev domain
with zero per-project DNS or proxy plumbing.

Bootstrap the shared infra layer once with 'pier install', then use
'pier init' per repo and 'pier up' / 'pier down' per worktree.`,
	Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
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
		newPsCmd(),
		newServeCmd(),
		newWatchCmd(),
		newGCCmd(),
		newClientCmd(),
		newDoctorCmd(),
		newWorktreeCmd(),
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
