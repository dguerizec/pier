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

	"github.com/dguerizec/pier/internal/manifest"
)

// Apply runs both passes (symlinks then snapshots). Idempotent. No-op when
// current == primary; missing sources are skipped silently rather than
// failing the up flow on a non-essential entry.
func Apply(primary, current string, mat manifest.Materialize, out io.Writer) error {
	if primary == current {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	for _, entry := range mat.Symlinks {
		target := filepath.Join(primary, entry)
		link := filepath.Join(current, entry)
		result, err := ensureSymlink(target, link)
		if err != nil {
			return fmt.Errorf("materialize symlink %s: %w", entry, err)
		}
		switch result.action {
		case actionCreated:
			fmt.Fprintf(out, "✓ symlinked %s → %s\n", entry, target)
		case actionBlocked:
			fmt.Fprintf(out, "! skipping symlink %s: %s (run `sudo rm -rf %s` to let pier manage it)\n", entry, result.reason, link)
		}
	}
	for _, entry := range mat.Snapshots {
		src := filepath.Join(primary, entry)
		dst := filepath.Join(current, entry)
		result, err := ensureSnapshot(src, dst)
		if err != nil {
			return fmt.Errorf("materialize snapshot %s: %w", entry, err)
		}
		switch result.action {
		case actionCreated:
			fmt.Fprintf(out, "✓ snapshot %s ← %s\n", entry, src)
		case actionBlocked:
			fmt.Fprintf(out, "! skipping snapshot %s: %s (run `sudo rm -rf %s` to let pier manage it)\n", entry, result.reason, dst)
		}
	}
	return nil
}

// action records what ensureSymlink / ensureSnapshot did so Apply can log
// proportionally — created on first up, silent on subsequent ups, but
// loud whenever something blocked the materialization (typically a
// root-owned empty dir from a previous broken docker bind mount).
type action int

const (
	actionNoop action = iota
	actionCreated
	actionBlocked
)

type result struct {
	action action
	reason string
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

// ensureSymlink creates link → target, reporting whether anything happened
// and why if it didn't.
//
// Existing entries are handled by kind:
//   - already a symlink, regular file, or non-empty dir → no-op (the user
//     or another tool owns it)
//   - empty dir → rmdir + symlink. Empty dirs are commonly side-effects of
//     a previous `docker compose up` that bind-mounted `./secrets:/...`
//     and the daemon auto-created the source path. If the rmdir fails
//     (e.g. root-owned dir from a pre-`user:` compose run), we surface a
//     blocked result so Apply can tell the user how to recover.
func ensureSymlink(target, link string) (result, error) {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return result{action: actionNoop}, nil
	} else if err != nil {
		return result{}, err
	}

	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fall through to create
	case err != nil:
		return result{}, err
	case info.Mode()&os.ModeSymlink != 0:
		return result{action: actionNoop}, nil
	case info.IsDir():
		if rmErr := os.Remove(link); rmErr != nil {
			return result{action: actionBlocked, reason: blockReason(link, rmErr)}, nil
		}
	default:
		return result{action: actionNoop}, nil
	}

	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return result{}, err
	}
	if err := os.Symlink(target, link); err != nil {
		return result{}, err
	}
	return result{action: actionCreated}, nil
}

// ensureSnapshot copies src into dst.
//
// For file snapshots the rule is "copy once": an existing dst is left
// alone so worktree-local edits survive subsequent ups.
//
// For directory snapshots we merge instead of skip: every entry from
// src that isn't already at dst lands there, and pre-existing entries
// in dst are preserved. This matters because dst can come from
// different sources — the docker daemon may have auto-created an empty
// dir on a previous up, the workload may have written its own state,
// or the user may have seeded a few files manually — and the user
// expects new snapshot content from primary to land in any of those
// cases. Skip-on-non-empty was a footgun: a primary that gained a new
// fixture file never propagated to existing worktrees.
//
// We check src BEFORE touching dst — otherwise a primary that's missing
// the snapshot source would have us remove the worktree's pre-existing
// empty dir, only to bail at the copy step and leave docker free to
// re-create dst as a root-owned dir.
func ensureSnapshot(src, dst string) (result, error) {
	srcInfo, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return result{action: actionNoop}, nil
	}
	if err != nil {
		return result{}, err
	}

	if srcInfo.IsDir() {
		return ensureSnapshotDir(src, dst)
	}
	return ensureSnapshotFile(src, dst, srcInfo)
}

// ensureSnapshotDir merges src into dst at file granularity. Existing
// entries at dst are preserved (file-level skip), missing entries are
// copied from src. The empty-dir-rmdir-then-copy fast path is kept so
// the root-owned bind-mount diagnostic still surfaces.
func ensureSnapshotDir(src, dst string) (result, error) {
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fall through to first-time copy
	case err != nil:
		return result{}, err
	case !info.IsDir():
		// dst is a file/symlink; user owns it, leave alone.
		return result{action: actionNoop}, nil
	case isEmpty(dst):
		if rmErr := os.Remove(dst); rmErr != nil {
			// Empty but unremovable — typically root-owned from an
			// earlier docker bind mount. Surface the diagnostic.
			return result{action: actionBlocked, reason: blockReason(dst, rmErr)}, nil
		}
	default:
		// Non-empty dst dir → merge new entries from src into it.
		copied, err := mergeTree(src, dst)
		if err != nil {
			return result{}, err
		}
		if copied == 0 {
			return result{action: actionNoop}, nil
		}
		return result{action: actionCreated}, nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return result{}, err
	}
	if err := copyTree(src, dst); err != nil {
		return result{}, err
	}
	return result{action: actionCreated}, nil
}

// ensureSnapshotFile copies a single-file snapshot. Existing dst is
// always preserved, regardless of whether it's a file or a directory —
// the user owns whatever's already at the path.
func ensureSnapshotFile(src, dst string, srcInfo os.FileInfo) (result, error) {
	if _, err := os.Lstat(dst); err == nil {
		return result{action: actionNoop}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return result{}, err
	}
	if err := copyFile(src, dst, srcInfo.Mode()); err != nil {
		return result{}, err
	}
	return result{action: actionCreated}, nil
}

func blockReason(path string, rmErr error) string {
	if info, err := os.Lstat(path); err == nil {
		if uid, ok := ownerUID(info); ok {
			return fmt.Sprintf("empty dir owned by uid %d (rmdir: %v)", uid, rmErr)
		}
	}
	return fmt.Sprintf("rmdir failed: %v", rmErr)
}

func isEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	return err != nil && len(names) == 0
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

// mergeTree walks src and copies entries that don't already exist at
// the corresponding path inside dst. Existing files/symlinks at dst
// win over the source — the worktree's mutations are sacred. Returns
// the number of leaf entries actually copied so callers can tell a
// no-op from a real merge.
func mergeTree(src, dst string) (copied int, err error) {
	err = filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if _, err := os.Lstat(target); err == nil {
			return nil // existing entry wins
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			copied++
			return nil
		}
		if err := copyFile(path, target, info.Mode()); err != nil {
			return err
		}
		copied++
		return nil
	})
	return copied, err
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
