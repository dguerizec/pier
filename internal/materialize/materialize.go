// Package materialize layers files from the primary worktree into a
// secondary worktree before the workload starts. DESIGN §5.6 covers both
// symlinks and snapshots; MVP implements symlinks only — snapshot copies
// (data-dev/) come with the next iteration.
package materialize

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Apply walks the symlink list in mat and creates each link in current
// pointing at the same path under primary. No-op when current == primary
// (we never symlink files onto themselves) or when the source is missing.
func Apply(primary, current string, mat manifest.Materialize, out io.Writer) error {
	if primary == current {
		return nil
	}
	for _, entry := range mat.Symlinks {
		target := filepath.Join(primary, entry)
		link := filepath.Join(current, entry)
		created, err := ensureSymlink(target, link)
		if err != nil {
			return fmt.Errorf("materialize symlink %s: %w", entry, err)
		}
		if created && out != nil {
			fmt.Fprintf(out, "✓ symlinked %s → %s\n", entry, target)
		}
	}
	return nil
}

// ensureSymlink reports whether it actually created the link (so callers can
// log only on first materialization, not every up).
func ensureSymlink(target, link string) (bool, error) {
	if _, err := os.Lstat(link); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		// Source missing on primary — skip silently rather than create a
		// dangling symlink the workload will trip on.
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return false, err
	}
	if err := os.Symlink(target, link); err != nil {
		return false, err
	}
	return true, nil
}
