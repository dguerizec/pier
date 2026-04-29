---
name: pier
description: Use this skill in any repository that has a `.pier.toml` file at its root. pier is a CLI that gives every git worktree a stable URL on a local dev domain via traefik + dnsmasq. When .pier.toml is present, prefer pier commands over their docker / git equivalents for workload and worktree lifecycle, and respect pier's manifest conventions described below.
---

# pier — workflow for AI coding assistants

You are working in a repository that uses [pier](https://github.com/dguerizec/pier).
The presence of `.pier.toml` at the repo root means workloads in this project
are meant to run under pier's local dev domain (`*.test`, `*.dev`, etc.) with
one URL per git worktree.

## When to apply this skill

- The repository contains a `.pier.toml` file at its root.
- The user asks anything about running, building, deploying, or stopping
  this project locally.
- The user asks to create or remove a git worktree in this project.

## Core mental model

- **One URL per worktree.** pier derives a slug from the branch name
  (`feat/x` → `x`) and turns it into `<slug>.<base_domain>` (and one
  sub-domain per exposed service: `<host>.<slug>.<base_domain>`).
- **The manifest is portable.** `base_domain` typically uses the
  `{pier.tld}` token (e.g. `myapp.{pier.tld}`) so the same `.pier.toml`
  works whether a contributor installed pier on `.test`, `.dev`, or
  anything else. **Never replace `{pier.tld}` with a literal value.**
- **Workloads are docker compose under the hood.** pier writes a
  `.pier/compose.override.yml` at `pier up` time that injects traefik
  labels, container names, ports reset, and templated env vars. **Never
  edit that file by hand** — it is regenerated and your edits will be
  lost.
- **State lives in three places**: git (worktrees), docker (containers),
  the pier state DB (`~/.config/pier/state.db`). pier orchestrates the
  three. Don't manipulate them directly when a pier command exists.

## Daily commands

Run these from inside the worktree you want to operate on (no flags
needed in the common case — pier resolves project + slug from the cwd).

| Task | pier command | Do NOT use |
|---|---|---|
| Start the workload | `pier up` | `docker compose up`, `make run` |
| Stop the workload | `pier down` | `docker compose down`, `docker stop` |
| Print the workload's URL | `pier url` (`--all` for every URL) | grep the manifest |
| Tail logs | `pier logs [-f] [--tail N]` | `docker compose logs` |
| Inspect containers | `pier ps` (passes through to compose) | `docker ps` (acceptable but less scoped) |
| List every active workload | `pier ls` | querying state.db directly |

`--slug X` on any of `up/down/url/logs/ps` targets a different worktree
without `cd`. `X` can be a slug, a branch name, or a worktree path /
basename — pier resolves all three.

## Worktrees

**This is the #1 thing AI assistants get wrong about pier.** Never use
`git worktree remove` without first running `pier down` in that
worktree, or you will leave orphaned containers, traefik routes, and
DNS records behind. pier provides commands that do both correctly.

| Task | pier command | Do NOT use |
|---|---|---|
| Create a worktree | `pier worktree add <name>` (or `<path>`) | `git worktree add` |
| Remove a worktree | `pier worktree rm <name>` | `git worktree remove` |
| Bulk remove all secondary worktrees | `pier worktree clean` | a loop over `git worktree remove` |

`pier worktree add <name>`:
- Resolves bare names against `manifest.worktree.dir` (e.g.
  `worktrees/<name>`) so you can stay short.
- Forks from `manifest.worktree.base_ref` (else `main`/`master`).
- Pre-creates snapshot dirs as the host user (avoids docker creating
  them as root and locking the user out).
- Materializes symlinks (`.env`, `secrets/`) and snapshots
  (`data-dev/`) from the primary worktree.
- `--up` chains `pier up` after materialization.
- `-b <branch>`, `--from <ref>` mirror the `git worktree add` flags.

`pier worktree rm <name>`:
- Runs `pier down` first (best-effort).
- `--purge` runs `pier down --purge` to wipe per-worktree snapshots.
- `--force` passes through to `git worktree remove --force`.

If for some reason you must use `git worktree remove` directly,
`pier down` in the worktree FIRST, then remove. Then check `pier ls`
and `pier gc` (when implemented) for any leftover state.

## Manifest reference (`.pier.toml`)

`pier init` generates the project / stack / expose / worktree blocks
from the compose file, but it does **not** generate `[env.<service>]` —
those entries are the main thing you'll add by hand. Read this section
in full before editing the manifest.

### Full annotated example

```toml
[project]
name        = "myapp"                # DNS label; becomes the workload sub-domain root
base_domain = "myapp.{pier.tld}"     # KEEP {pier.tld} — never inline a literal TLD

[stack]
kind    = "compose"                  # only `compose` supported today
file    = "docker-compose.dev.yml"   # path relative to the worktree toplevel
service = "front"                    # the service that ALSO answers at the bare <slug>.<base>;
                                     # if empty, no alias is emitted

# match_host_uid = true              # optional; injects user: "<uid>:<gid>" into every
                                     # exposed service so bind-mounted host paths stay
                                     # writable when the image's default UID differs.

# Each [[expose]] entry tells pier to publish one compose service behind
# traefik. The container-side port is what traefik forwards to over the
# pier docker network; host port bindings in the compose file are stripped
# by pier at up time so multi-worktree runs don't collide.
[[expose]]
service = "front"
port    = 8080

[[expose]]
service = "api"
port    = 8000
# host  = "backend"                  # optional; defaults to the service name.
                                     # → backend.<slug>.<base> instead of api.<slug>.<base>

# [env.<service>] values are templated by pier at `pier up` time and
# injected as environment variables into that service's container, so
# the app reads the right URL for the current worktree without knowing
# pier exists. Compose merges environment dict-wise, so these override
# whatever the user's docker-compose.dev.yml set for the same key.
[env.front]
API_PUBLIC_URL = "{url.api}"         # → http://api.<slug>.<base> at runtime
PUBLIC_URL     = "{url.default}"     # → http://<slug>.<base> (requires stack.service)

[materialize]
symlinks  = [".env", "secrets/"]     # shared with the primary worktree (read-only intent)
snapshots = ["data-dev/"]            # copied per worktree (mutable, isolated)

[worktree]
dir      = "./worktrees"             # `pier worktree add <name>` creates ./worktrees/<name>
base_ref = "main"                    # new branches fork from this ref
```

### Templating tokens

`pier init` does not write any `[env.<service>]` — you add them when an
app needs to know the URL of a sibling service for the current
worktree. Use these tokens in `[env.<service>]` values:

| Token | Expands to | Notes |
|---|---|---|
| `{slug}` | the workload's slug (DNS label) | derived from branch |
| `{base_domain}` | post-template base domain (e.g. `myapp.test`) | |
| `{pier.tld}` | the installed pier TLD | also valid in `project.base_domain` |
| `{host.<service>}` | `<host>.<slug>.<base>` | `<service>` must appear in `[[expose]]` |
| `{url.<service>}` | `http://<host>.<slug>.<base>` | same |
| `{host.default}` | `<slug>.<base>` (bare slug alias) | requires `stack.service` set |
| `{url.default}` | `http://<slug>.<base>` | same |

Unknown tokens fail the `pier up` with a clear error — typos surface
immediately rather than silently producing broken values.

`project.base_domain` is read at startup (before workload context
exists), so it accepts `{pier.tld}` only.

### Common `[env.<service>]` patterns

Two-tier "front calls the API":

```toml
[[expose]]
service = "front"
port    = 8080

[[expose]]
service = "api"
port    = 8000

[env.front]
API_URL = "{url.api}"               # browser-facing; the front injects it into pages
```

Backend that announces its public URL (e.g. for OAuth callbacks, share
links, OpenAPI servers list):

```toml
[env.api]
PUBLIC_URL = "{url.default}"        # if stack.service = "api"
# or
PUBLIC_URL = "{url.api}"            # explicit per-service host
```

Worker that needs the API URL (no `[[expose]]` for the worker — pier
emits the env block even for non-exposed services):

```toml
[env.worker]
API_URL = "{url.api}"
```

### `[materialize]` — symlinks vs snapshots

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

### What `pier init` does and doesn't do

- ✅ Asks which detected services to `[[expose]]` and at what port/host.
- ✅ Picks a default service for the bare-slug alias.
- ✅ Writes `[stack]`, `[[expose]]`, `[worktree]`, sane `base_domain`.
- ❌ Does NOT prompt for `[env.<service>]` — too project-specific.
- ❌ Does NOT add `[materialize]` entries — add them when the app
  expects `.env`, secrets, or a per-worktree mutable data dir.
- ❌ Does NOT add `[hooks]`, `[watch]`, or `match_host_uid` — opt-in.

## Anti-patterns to avoid

1. **Editing `.pier/compose.override.yml`.** Regenerated at every
   `pier up`. Edit the user's `docker-compose.dev.yml` or `.pier.toml`
   instead.
2. **Inlining `base_domain = "myapp.test"`.** Breaks portability across
   contributors with different TLDs. Use `myapp.{pier.tld}`.
3. **`git worktree remove` without `pier down` first.** Leaves orphan
   containers and DNS records.
4. **Adding `default = true` to an `[[expose]]` entry.** That field
   doesn't exist. Designate the default by setting `stack.service` to
   the service name.
5. **Hardcoding URLs in source code.** Use env vars (`API_PUBLIC_URL`,
   etc.) and let pier inject the right value per worktree via
   `[env.<service>]`.
6. **Running `docker compose up` directly.** Bypasses traefik
   registration, host port stripping, and per-worktree container
   names — multi-worktree runs will collide.

## Common one-liners

```bash
# Spin up a feature branch in its own worktree, ready to demo
pier worktree add feat-x --up
pier url --slug feat-x       # → http://feat-x.myapp.test (or .dev, etc.)

# Tear it all down
pier worktree rm feat-x --purge

# What's currently running, where?
pier ls

# Inspect another worktree's containers from anywhere
pier ps --slug feat-x
```

## When pier doesn't fit

- **Browser code with hardcoded `localhost:PORT`.** pier can't rewrite
  values inside browser-side bundles. Refactor the app to read its API
  URL from an env var, then inject it via `[env.<service>]`.
- **Apps that absolutely need stable host port bindings** (e.g. another
  process on the host expects to reach the container at `localhost:PORT`).
  pier strips host ports in its override to avoid multi-worktree
  collisions; if you must keep them, you can only run one worktree at a
  time of that project under pier.
- **Production hosting.** pier is for dev/preview only. Don't touch
  production rollout paths from a pier workflow.
