# `[materialize]` — symlinks, snapshots, and lifecycle hooks

## Symlinks vs snapshots

```toml
[materialize]
# Pointers shared with the primary worktree. Changes on either side are
# visible everywhere. Use for static config and read-only secrets.
symlinks  = [".env", "secrets/", "config/local.json"]

# Per-worktree copies, taken from the primary at the first `pier up`.
# Each worktree mutates its own copy in isolation. Use for SQLite files,
# uploads dirs, build caches — anything the running app writes to.
snapshots = ["data-dev/", "uploads/", ".cache/"]
```

Two ways to populate files in a secondary worktree from the primary,
with opposite semantics:

| | `symlinks` | `snapshots` |
|---|---|---|
| Storage | shared with the primary | duplicated per worktree |
| Edit from secondary | propagates to primary (and every other worktree) | local to that worktree |
| Edit from primary | visible everywhere | NOT propagated (snapshot is point-in-time) |
| `pier down --purge` | preserved | removed |
| Use for | static config, read-only secrets | mutable per-branch data |

**Lifecycle of snapshots:**
- Primary worktree: snapshots ignored — the primary IS the source of truth.
- First `pier up` in a secondary: `cp -r primary/<path> → worktree/<path>`.
  If the path doesn't exist on the primary, pier pre-creates an empty
  dir as the host user (so docker doesn't bind-mount-create it as root).
- Later `pier up`: no-op — pier respects local edits.
- `pier down --purge`: wipes the secondary's snapshots only. Primary
  untouched.

**Typical entries:**
- `.env`, `secrets/`, `config/local.json` → `symlinks`. Same value
  everywhere, edits to the primary should fan out instantly.
- `data-dev/` (SQLite db file, uploads dir, build cache, mutable
  fixtures) → `snapshots`. Each branch needs its own copy so it can
  mutate without trashing other branches' state.

**Pitfalls:**
- Large directories: snapshots use plain `cp -r`, no COW/reflink. A 5
  GB snapshot duplicates 5 GB on disk per worktree at the first `pier
  up`. For heavy datasets, prefer a docker named volume + a small
  `dump/restore` hook (or just a symlink, accepting the shared-state
  trade-off).
- No auto re-sync: a primary update after the snapshot was taken
  doesn't reach existing worktrees. Re-sync explicitly: `pier down
  --purge && pier up` recreates the snapshot from the current primary.
- A `git worktree add` (instead of `pier worktree add`) skips the snapshot
  pre-create step. The next `pier up` may then have docker bind-mount-
  create the path as root, locking the host user out. Recovery: `sudo
  rm -rf <path>` then `pier up`. Avoid by using `pier worktree add`.

## `post_create` / `pre_remove` hooks

Shell commands tied to the **worktree** lifecycle (`pier worktree add` /
`pier worktree rm`), not to `pier up` / `pier down`. Live under
`[materialize]` because the canonical use cases are bound to the
per-worktree filesystem layout: seeding a freshly snapshotted DB, dumping
it to a backup file before purge.

```toml
[materialize]
snapshots   = ["data-dev/"]                 # the per-worktree DB dir
post_create = ["./scripts/seed-db.sh"]      # run after symlinks/snapshots, before --up
pre_remove  = ["./scripts/backup-db.sh"]    # run BEFORE pier down (workload still up)
```

**Execution model:**
- Each entry is a string passed to `sh -c`. Lists are run in order; the
  first non-zero exit aborts the sequence.
- Cwd is the worktree being acted on (the new one for `post_create`,
  the one being removed for `pre_remove`).
- Stdout/stderr stream live to the user terminal — no buffering, so
  multi-second seed/backup operations show progress as they happen.

**Env exposed to scripts** (always set, possibly empty for a missing value):

| Var | Meaning |
|---|---|
| `PIER_WORKTREE_PATH` | absolute path of the worktree the hook is acting on |
| `PIER_PRIMARY_PATH` | absolute path of the primary worktree |
| `PIER_SLUG` | DNS label derived from the branch |
| `PIER_BRANCH` | raw branch name |
| `PIER_BASE_DOMAIN` | post-template base domain (e.g. `myapp.test`); empty if pier infra not loadable |
| `PIER_PROJECT_NAME` | `[project].name` |

**Failure behaviour:**
- `post_create` fails → pier force-removes the worktree and (only if it
  created the branch in this same call) deletes the branch. Net effect:
  the filesystem and git state look like the `pier worktree add` never
  ran. Pass `--ignore-hook-errors` to keep the worktree on failure.
- `pre_remove` fails → pier aborts before `pier down` and `git worktree
  remove`. The worktree stays usable so you can fix the script and
  retry. Pass `--ignore-hook-errors` to remove anyway.
- A script can swallow its own non-fatal errors and `exit 0` to opt out
  of the rollback for cases pier shouldn't treat as failures.

**Pitfalls:**
- The hook script must exist in the worktree's checked-out tree at the
  point it runs. If you put scripts in `scripts/` and check out a
  branch that pre-dates the script being committed, `post_create` will
  fail with `not found`. Two fixes: commit the scripts on every branch
  that uses them, or list `scripts/` in `[materialize].symlinks` so the
  primary's copy is materialized into the new worktree before the hook
  runs.
- `pre_remove` runs **before** `pier down` so the workload is still
  reachable (DB up, services responding). Don't rely on the workload
  being down inside a `pre_remove` script.
- `pier worktree rm --skip-down` still runs `pre_remove`. The "workload
  still up" guarantee then depends on whether the user actually left it
  running. If your `pre_remove` does a `pg_dump`, document that it
  needs the container alive — don't assume `--skip-down` means the
  caller already dumped.
- `pier worktree clean` runs each worktree's `pre_remove` serially. If
  every script writes to the same path (e.g. `backups/dump.sql`),
  later worktrees clobber earlier ones. Namespace by `$PIER_SLUG`:
  `pg_dump > "backups/${PIER_SLUG}.sql"`.
- `[hooks]` (top-level) is a different block aimed at the `pier up` /
  `pier down` lifecycle. Don't confuse the two.
