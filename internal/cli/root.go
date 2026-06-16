package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Populated at build time via -ldflags by goreleaser. Defaults are used for
// `go install` / `go build` so a self-built binary still reports something.
const devBaseVersion = "v0.0.1-rc1"

var (
	version = "v0.0.1-rc1-dev"
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
	Version:       buildVersionString(),
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

func buildVersionString() string {
	return fmt.Sprintf("%s (commit %s, built %s)", buildVersion(), buildCommit(), buildDate())
}

func buildVersion() string {
	v := version
	if v == devBaseVersion+"-dev" && buildSetting("vcs.modified") == "true" {
		return v + "+dirty"
	}
	return v
}

func buildCommit() string {
	if commit != "none" {
		return commit
	}
	if v := buildSetting("vcs.revision"); v != "" {
		if len(v) > 12 {
			return v[:12]
		}
		return v
	}
	return commit
}

func buildDate() string {
	if date != "unknown" {
		return date
	}
	if v := buildSetting("vcs.time"); v != "" {
		return v
	}
	return date
}

func buildSetting(key string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}
