// Package skill embeds the Claude Code skill that teaches AI assistants
// how to operate inside a pier-managed repository. `pier install` drops
// the embedded SKILL.md into ~/.claude/skills/pier/SKILL.md so any
// assistant working in any pier project picks up the conventions
// automatically — no per-project commit needed.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var Body []byte

// userRel is the path under the user's home where Claude Code looks for
// user-level skills.
const userRel = ".claude/skills/pier/SKILL.md"

// UserPath returns the absolute install location for the user-level
// skill (~/.claude/skills/pier/SKILL.md).
func UserPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, userRel), nil
}

// Install writes the embedded skill to dst, overwriting any existing
// file. Used by `pier install`, which is itself idempotent (recreate
// semantics) — re-running keeps the skill in sync with the binary.
// Project-local overrides under <repo>/.claude/skills/pier/ still take
// precedence at lookup time, so this never stomps a customized skill.
func Install(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, Body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// Uninstall removes the skill at dst. Returns whether anything was
// removed; missing files are not an error.
func Uninstall(dst string) (removed bool, err error) {
	err = os.Remove(dst)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Best-effort: clean the now-empty parent dir, but only if empty.
	_ = os.Remove(filepath.Dir(dst))
	return true, nil
}
