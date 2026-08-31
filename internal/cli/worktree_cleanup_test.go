package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorktreeDownArgsIncludesCleanupByDefault(t *testing.T) {
	got := strings.Join(worktreeDownArgs(wtRmOpts{}), " ")
	if got != "down --purge --volumes --images" {
		t.Fatalf("worktree down args = %q", got)
	}
}

func TestWorktreeDownArgsCanKeepResources(t *testing.T) {
	tests := []struct {
		name string
		opts wtRmOpts
		want string
	}{
		{name: "both", opts: wtRmOpts{keepVolumes: true, keepImages: true}, want: "down --purge"},
		{name: "volumes", opts: wtRmOpts{keepVolumes: true}, want: "down --purge --images"},
		{name: "images", opts: wtRmOpts{keepImages: true}, want: "down --purge --volumes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(worktreeDownArgs(tt.opts), " "); got != tt.want {
				t.Fatalf("worktree down args = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorktreeCleanupCannotBeSkippedByDefault(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := runWorktreeRm(cmd, "unused", wtRmOpts{skipDown: true})
	if err == nil || !strings.Contains(err.Error(), "--skip-down requires") {
		t.Fatalf("skip-down cleanup error = %v", err)
	}
}

func TestWorktreeSnapshotPurgeCanSkipWorkloadTeardown(t *testing.T) {
	err := validateWorktreeRmOpts(wtRmOpts{
		skipDown:    true,
		keepVolumes: true,
		keepImages:  true,
	})
	if err != nil {
		t.Fatalf("skip-down snapshot cleanup validation = %v", err)
	}
}

func TestWorktreeRetentionFlagsAreAvailable(t *testing.T) {
	for _, cmd := range []*cobra.Command{newWorktreeRmCmd(), newWorktreeCleanCmd()} {
		for _, name := range []string{"keep-volumes", "keep-images"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s is missing --%s", cmd.CommandPath(), name)
			}
		}
	}
}

func TestLegacyWorktreePurgeFlagRemainsAccepted(t *testing.T) {
	for _, cmd := range []*cobra.Command{newWorktreeRmCmd(), newWorktreeCleanCmd()} {
		flag := cmd.Flags().Lookup("purge")
		if flag == nil || flag.Deprecated == "" {
			t.Errorf("%s should retain --purge as a deprecated compatibility flag", cmd.CommandPath())
		}
	}
}
