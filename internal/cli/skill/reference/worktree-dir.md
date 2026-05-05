# Worktree dir resolution

The directory `pier worktree add <name>` resolves bare names against is
**not normally pinned in `.pier.toml`**. It's a per-user / per-host
preference, not a project setting, so different contributors can keep
their worktrees wherever they like without touching the committed
manifest.

## Resolution order

Highest priority first:

1. `.pier.local.toml` `[worktree].dir` — local override, gitignored.
2. `.pier.toml` `[worktree].dir` — committed project pin. **Only set
   this when the user explicitly asks to commit a project-wide
   default.** `pier init` deliberately leaves it unset and instead
   persists `--worktree-dir` to prefs.
3. `~/.config/pier/prefs.toml` `worktree_dir` — per-user default,
   written by `pier init --worktree-dir <path>` and reused across all
   pier projects on the host.
4. Built-in fallback: `.pier/worktrees` (sibling of the primary
   worktree). Bare names always resolve to *somewhere* predictable.

## Guideline for AI assistants

Do NOT add `[worktree].dir` to `.pier.toml` proactively. If the user
wants to change where worktrees land:

- Prefer `pier init --worktree-dir <path>` (writes to prefs).
- Or `.pier.local.toml` (local override, not committed).
- Only write it into `.pier.toml` when the user explicitly says they
  want to commit it.
