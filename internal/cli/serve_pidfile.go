package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/LeoPartt/pier/internal/infra"
)

// pidfilePath returns where the running serve process records its PID.
// Lives next to other pier state under $XDG_CONFIG_HOME/pier so doctor
// and `serve upgrade` can find it without a global registry.
func pidfilePath(paths *infra.Paths) string {
	return filepath.Join(paths.Root, "pier-serve.pid")
}

// writePidfile drops the current PID. We don't use O_EXCL: a stale
// pidfile from a crashed serve is benign (writePidfile just overwrites
// it), and refusing to start when one exists would block recovery.
func writePidfile(paths *infra.Paths) error {
	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		return err
	}
	body := strconv.Itoa(os.Getpid()) + "\n"
	return os.WriteFile(pidfilePath(paths), []byte(body), 0o644)
}

func removePidfile(paths *infra.Paths) {
	_ = os.Remove(pidfilePath(paths))
}

// readRunningPID returns the PID recorded in the pidfile, after
// verifying the process is actually alive. Returns (0, nil) when no
// daemon is running so callers can produce a friendly error.
func readRunningPID(paths *infra.Paths) (int, error) {
	raw, err := os.ReadFile(pidfilePath(paths))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pidfile %s: malformed contents %q", pidfilePath(paths), raw)
	}
	// signal 0 = "is the process reachable from us?" — does not deliver
	// anything, just probes existence and permissions.
	if err := syscall.Kill(pid, 0); err != nil {
		// Stale: pidfile points at a dead process.
		return 0, nil
	}
	return pid, nil
}
