# Bootstrapping a project — `pier init`

When a repository has a docker-compose file but no `.pier.toml`, the
project isn't pier-managed yet. **Don't hand-write the manifest.** Run
`pier init` — the wizard inspects the compose file, fills in the
project / stack / expose / worktree blocks, gitignores `.pier.local.toml`
and `.pier/`, and registers the project in pier's state DB so the REST
API and dashboard pick it up.

Hand-written manifests are a common source of pier bugs (typos in
`base_domain`, missing `match_host_uid`, wrong `kind`, slug collisions
between expose hosts). The wizard avoids all of these by construction.

## Preconditions

Before `pier init` will succeed:

- The repository is a **git repository**, and pier resolves its
  primary worktree. Running init from a secondary worktree works but
  the manifest lives there only — prefer the primary.
- A **compose file is reachable** (auto-detected:
  `docker-compose.dev.yml`, `docker-compose.yml`, `compose.yml`, etc.).
  Override with `--file path/to/compose.yml` when auto-detection picks
  the wrong one.
- **pier infra is installed** (`pier install` was run on the host)
  when you want `{pier.tld}` interpolation to work at `pier up` time.
  `pier init` itself doesn't require infra — the manifest can be
  written before infra is installed — but `pier up` will fail until
  it is.

## Unattended invocation (the agent default)

AI assistants typically run without a TTY. Use `--yes` to skip every
prompt and accept the wizard's defaults:

```bash
pier init --yes
```

This produces a `.pier.toml` with:

- `[project].name` = repository directory name.
- `[project].base_domain` = `<name>.{pier.tld}`.
- `[stack].kind` = `compose`, `[stack].file` = auto-detected compose
  file (relative to repo root), `[stack].service` = first exposed
  service (alphabetical).
- `[stack].match_host_uid` = `true` (safe default; see
  [manifest.md](manifest.md) "when to set true vs false").
- `[[expose]]` entries for every compose service that maps a port.
- `[worktree].base_ref` = detected `main` or `master`.
- `[worktree].dir` is **not written** (per-user preference; falls back
  to prefs.toml then `.pier/worktrees`).

`.pier.toml` is committed by default (the manifest is portable across
contributors thanks to `{pier.tld}`); pass `--private` to gitignore it.

## When to override defaults

Common one-off flags for the unattended path:

| Situation | Flag |
|---|---|
| Repo dir name doesn't match the desired DNS label | `--name <label>` |
| Want a fixed TLD (rare; usually leave `{pier.tld}`) | `--domain <name>.<tld>` |
| Multiple compose files, want a specific one | `--file path/to/compose.yml` |
| Bare-slug alias should target a specific service | `--service <name>` |
| Image hard-codes a non-root user that owns its own data dir (postgres, mysql) | `--no-match-host-uid` |
| Project shouldn't commit `.pier.toml` (closed-source overlay, fork) | `--private` |
| Worktree dir should be project-pinned (most projects: skip — it's a per-user preference) | `--worktree-dir <path>` (also writes to `~/.config/pier/prefs.toml`) |

The default branch ref (`main`/`master`) is auto-detected; only set
`--base-ref` when the project's mainline branch has a different name
(e.g. `develop`, `trunk`).

## Re-init

Re-running `pier init` on a project that already has a `.pier.toml` is
safe: the wizard preserves user-curated sections (`[env.<service>]`,
`[materialize]`, `[hooks]`, `[watch]`) and only rewrites the
wizard-owned blocks (`[project]`, `[stack]`, `[[expose]]`,
`[worktree].base_ref`).

`match_host_uid` is wizard-owned but **inherited from the existing
manifest** when the key was explicitly set. Re-init never silently
flips the value: a manifest with `match_host_uid = false` keeps
`false` on re-init; a legacy manifest without the key falls back to
the safe default (`true`).

Re-run after editing the compose file to add a service, change a port,
or rename a service — the wizard picks up the changes and updates
`[[expose]]` accordingly.

## What `pier init` does NOT generate

Even after `pier init`, the agent typically still has to add:

- **`[env.<service>]`** when a service needs to know the URL of a
  sibling service for the current worktree (front → API,
  worker → API, OAuth callback URL, etc.). Use the templating tokens
  documented in [manifest.md](manifest.md).
- **`[materialize].symlinks`** for shared static config / read-only
  secrets (`.env`, `secrets/`) that should be visible from every
  secondary worktree without duplication.
- **`[materialize].snapshots`** for per-worktree mutable data (SQLite
  files, uploads dirs, build caches) that each worktree needs to
  isolate.
- **`[materialize].post_create` / `pre_remove`** hooks for
  worktree-lifecycle scripts (DB seed on add, dump on remove). See
  [materialize.md](materialize.md).
- **`[hooks].pre_up` / `post_up` / `pre_down` / `post_down`** for
  workload-lifecycle scripts (build before up, smoke test after up,
  notify on down). See [materialize.md](materialize.md).

These are project-specific by nature — the wizard can't infer them
from the compose file. Add them by hand in the manifest after init.

## After init

The agent should:

1. Verify the manifest is sane (cat `.pier.toml`, check the detected
   service set matches the user's mental model).
2. Add `[env.<service>]` entries if the app reads sibling URLs from
   env (most do).
3. Add `[materialize]` entries if the app expects `.env`, secrets, or
   per-worktree mutable data.
4. Run `pier up` and confirm the URL prints. The first up on a new
   project is the smoke test that everything is wired correctly.

If `pier up` fails with `Permission denied` on a bind-mounted path,
the agent missed the `match_host_uid` decision — re-run init or edit
the manifest by hand to set it correctly.
