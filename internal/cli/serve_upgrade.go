package cli

import (
	"fmt"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/dguerizec/pier/internal/infra"
)

func newServeUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Trigger a graceful binary swap on the running pier serve",
		Long: `Locates the running pier serve via its pidfile and sends SIGUSR2.
The daemon re-execs in place on the new binary, keeping the listening
socket bound across the swap so the API stays reachable.

In-flight HTTP requests die mid-response: the process image is
replaced atomically. The dashboard's SSE clients reconnect within a
second; synchronous clients should retry.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := infra.DefaultPaths()
			if err != nil {
				return err
			}
			pid, err := readRunningPID(paths)
			if err != nil {
				return err
			}
			if pid == 0 {
				return fmt.Errorf("no running pier serve found (pidfile %s missing or stale)", pidfilePath(paths))
			}
			if err := syscall.Kill(pid, syscall.SIGUSR2); err != nil {
				return fmt.Errorf("signal pid=%d: %w", pid, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ SIGUSR2 sent to pid=%d — daemon will swap binary in place\n", pid)
			return nil
		},
	}
}
