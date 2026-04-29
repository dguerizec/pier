package cli

import (
	"github.com/LeoPartt/pier/internal/infra"
	"github.com/LeoPartt/pier/internal/manifest"
)

// defaultWorktreeDir is what `pier worktree add <name>` resolves bare
// names against when neither the manifest nor the user's prefs.toml
// pin a location.
const defaultWorktreeDir = ".pier/worktrees"

// effectiveWorktreeDir resolves the worktree dir for `pier worktree
// add` / `rm`. Resolution order, highest priority first:
//
//   1. .pier.local.toml [worktree].dir — already merged into m by manifest.Load
//   2. .pier.toml [worktree].dir — committed project pin
//   3. ~/.config/pier/prefs.toml worktree_dir — per-user default
//   4. defaultWorktreeDir — built-in fallback so bare names always resolve
//
// The first three keep their precedence chain; (4) ensures `pier
// worktree add feat-x` always lands somewhere predictable instead of
// falling through to the cwd.
func effectiveWorktreeDir(m *manifest.Manifest) string {
	if m != nil && m.Worktree.Dir != "" {
		return m.Worktree.Dir
	}
	if dir := loadPrefsWorktreeDir(); dir != "" {
		return dir
	}
	return defaultWorktreeDir
}

func loadPrefsWorktreeDir() string {
	paths, err := infra.DefaultPaths()
	if err != nil {
		return ""
	}
	prefs, err := infra.LoadPrefs(paths)
	if err != nil {
		return ""
	}
	return prefs.WorktreeDir
}
