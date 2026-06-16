// Package skill embeds the agent skill that teaches AI assistants how to
// operate inside a pier-managed repository. `pier install` writes the
// canonical skill under ~/.agents/skills/pier/ and can expose it to
// agent-specific loaders through symlinks.
package skill

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Files holds the entire skill tree (SKILL.md + reference/*.md).
// Anthropic's skill format supports progressive disclosure: SKILL.md
// loads first, deeper docs are pulled in by Claude only when relevant.
//
//go:embed SKILL.md reference
var Files embed.FS

const (
	// canonicalUserRel is the neutral user-level location. Agent-specific
	// directories point at this path instead of carrying independent copies.
	canonicalUserRel = ".agents/skills/pier"

	claudeSkillsRel = ".claude/skills"
	skillName       = "pier"
)

// UserDir returns the canonical user-level install directory
// (~/.agents/skills/pier).
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, canonicalUserRel), nil
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

// LinkTarget is an agent-specific skill path that should point at the
// canonical pier skill when the parent skill directory already exists.
type LinkTarget struct {
	Agent string
	Dir   string
}

// DetectedLinkTargets returns agent-specific skill locations whose parent
// directories already exist. Unknown agents are intentionally ignored; the
// canonical ~/.agents/skills/pier install remains available for manual links.
func DetectedLinkTargets() ([]LinkTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	targets := []LinkTarget{
		{Agent: "claude", Dir: filepath.Join(home, claudeSkillsRel, skillName)},
		{Agent: "codex", Dir: filepath.Join(codexHome(home), "skills", skillName)},
	}

	out := targets[:0]
	for _, target := range targets {
		parent := filepath.Dir(target.Dir)
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			out = append(out, target)
		}
	}
	return slices.Clone(out), nil
}

func codexHome(home string) string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".codex")
}

// LinkStatus describes what currently exists at a detected agent-specific
// target path.
type LinkStatus int

const (
	LinkMissing LinkStatus = iota
	LinkCurrent
	LinkConflict
)

// LinkState reports whether target already points at canonical, is missing,
// or would need user approval before replacement.
func LinkState(target, canonical string) (LinkStatus, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return LinkMissing, nil
	}
	if err != nil {
		return LinkConflict, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return LinkConflict, nil
	}
	ok, err := symlinkPointsTo(target, canonical)
	if err != nil {
		return LinkConflict, err
	}
	if ok {
		return LinkCurrent, nil
	}
	return LinkConflict, nil
}

// Link replaces target with a symlink to canonical. Callers are responsible
// for asking before replacing conflicts.
func Link(target, canonical string) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Symlink(canonical, target)
}

func symlinkPointsTo(path, canonical string) (bool, error) {
	got, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(got) {
		got = filepath.Join(filepath.Dir(path), got)
	}
	got, err = filepath.Abs(got)
	if err != nil {
		return false, err
	}
	want, err := filepath.Abs(canonical)
	if err != nil {
		return false, err
	}
	return filepath.Clean(got) == filepath.Clean(want), nil
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
	// Best-effort: clean the now-empty parent (~/.agents/skills) only if empty.
	_ = os.Remove(filepath.Dir(dir))
	return true, nil
}
