// Package skill embeds the Claude Code skill that teaches AI assistants
// how to operate inside a pier-managed repository. `pier install` drops
// the embedded skill tree into ~/.claude/skills/pier/ so any assistant
// working in any pier project picks up the conventions automatically —
// no per-project commit needed.
package skill

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Files holds the entire skill tree (SKILL.md + reference/*.md).
// Anthropic's skill format supports progressive disclosure: SKILL.md
// loads first, deeper docs are pulled in by Claude only when relevant.
//
//go:embed SKILL.md reference
var Files embed.FS

// userRel is the directory under the user's home where Claude Code
// looks for user-level skills.
const userRel = ".claude/skills/pier"

// UserDir returns the absolute install directory for the user-level
// skill (~/.claude/skills/pier).
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, userRel), nil
}

// Install writes the embedded skill tree under dir, overwriting any
// existing files. Used by `pier install`, which is idempotent —
// re-running keeps the skill in sync with the binary. Project-local
// overrides under <repo>/.claude/skills/pier/ still take precedence at
// lookup time, so this never stomps a customized skill.
func Install(dir string) error {
	return fs.WalkDir(Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, p)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := Files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		return nil
	})
}

// Uninstall removes the skill directory at dir. Returns whether
// anything was removed; missing dirs are not an error.
func Uninstall(dir string) (removed bool, err error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	// Best-effort: clean the now-empty parent (~/.claude/skills) only if empty.
	_ = os.Remove(filepath.Dir(dir))
	return true, nil
}
