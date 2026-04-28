// Package materialize layers files from the primary worktree into a
// secondary worktree before the workload starts (DESIGN §5.6). Two flavors:
//
//   - symlinks (.env, secrets/) — shared state, the worktree just points at
//     the primary's copy.
//   - snapshots (data-dev/) — per-worktree copy so each branch can mutate
//     its own DB / fixtures without disturbing the others.
package materialize

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LeoPartt/pier/internal/manifest"
)

// Apply runs both passes (symlinks then snapshots). Idempotent. No-op when
// current == primary; missing sources are skipped silently rather than
// failing the up flow on a non-essential entry.
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
	for _, entry := range mat.Snapshots {
		src := filepath.Join(primary, entry)
		dst := filepath.Join(current, entry)
		copied, err := ensureSnapshot(src, dst)
		if err != nil {
			return fmt.Errorf("materialize snapshot %s: %w", entry, err)
		}
		if copied && out != nil {
			fmt.Fprintf(out, "✓ snapshot %s ← %s\n", entry, src)
		}
	}
	return nil
}

// Purge removes snapshot copies from current (DESIGN §3.3 `pier down --purge`).
// Symlinks are NEVER removed — they point back at the primary worktree and
// deleting them would clobber shared state. Snapshots that don't exist are
// no-ops.
func Purge(current string, mat manifest.Materialize, out io.Writer) error {
	for _, entry := range mat.Snapshots {
		path := filepath.Join(current, entry)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// A snapshot becoming a symlink would be unexpected (the user
		// changed the manifest mid-flight); refuse to touch it.
		if info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(out, "! skipping %s: now a symlink, not a snapshot\n", entry)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("purge %s: %w", entry, err)
		}
		fmt.Fprintf(out, "✓ purged snapshot %s\n", entry)
	}
	return nil
}

// ensureSymlink reports whether it actually created the link (so callers
// can log only on first materialization, not every up).
//
// Existing entries are handled by kind:
//   - already a symlink, regular file, or non-empty dir → leave alone
//     (the user or another tool owns it)
//   - empty dir → rmdir + symlink. Empty dirs are commonly side-effects of
//     a previous `docker compose up` that bind-mounted `./secrets:/...`
//     and the daemon auto-created the source path.
func ensureSymlink(target, link string) (bool, error) {
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fall through to create
	case err != nil:
		return false, err
	case info.Mode()&os.ModeSymlink != 0:
		return false, nil
	case info.IsDir():
		// os.Remove on a directory only succeeds when empty — that's the
		// signal we use here, no extra readdir needed.
		if rmErr := os.Remove(link); rmErr != nil {
			return false, nil
		}
	default:
		return false, nil
	}

	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
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

// ensureSnapshot copies src into dst on first up. Same docker-bind-mount
// safety net as ensureSymlink: an empty dir is treated as "absent" since
// the daemon often pre-creates it.
func ensureSnapshot(src, dst string) (bool, error) {
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fall through to copy
	case err != nil:
		return false, err
	case info.IsDir():
		if rmErr := os.Remove(dst); rmErr != nil {
			return false, nil
		}
	default:
		return false, nil
	}

	srcInfo, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if srcInfo.IsDir() {
		return true, copyTree(src, dst)
	}
	return true, copyFile(src, dst, srcInfo.Mode())
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode())
		case info.Mode()&os.ModeSymlink != 0:
			// Preserve symlinks rather than dereferencing.
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode())
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
