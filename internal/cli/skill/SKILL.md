---
name: pier
description: >-
  MUST invoke whenever the repo root has a `.pier.toml`, OR before running
  `git worktree add`, `docker compose up/down/logs`, or `pier init` in such a
  repo. Without this skill, agents fall back to raw `git worktree add` /
  `docker compose` and BREAK pier projects — they skip materialize
  (per-worktree symlinks/snapshots), the lifecycle hooks (post_create /
  pre_remove / pre_up / post_up / pre_down / post_down), and the per-worktree
  slug DNS, leaving the worktree with no URL and no seeded data. Also invoke
  when the user asks to add pier to a docker-compose project (run `pier init`,
  never hand-write the manifest). Trigger on: any `.pier.toml` at
  cwd-or-ancestor, any user mention of pier / worktree / `*.test`,`*.dev` URL.
---

# pier — workflow for AI coding assistants

You are working in a repository that uses [pier](https://github.com/dguerizec/pier).
The presence of `.pier.toml` at the repo root means workloads in this project
are meant to run under pier's local dev domain (`*.test`, `*.dev`, etc.) with
one URL per git worktree.

## When to apply

- The repository contains a `.pier.toml` file at its root.
- The user asks anything about running, building, deploying, or stopping
  this project locally.
- The user asks to create or remove a git worktree in this project.
- The repository has a `docker-compose*.yml` but no `.pier.toml`, and
  the user wants to add pier — run `pier init`, don't hand-write the
  manifest.

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

## Bootstrapping (`pier init`)

When a repository has a docker-compose file but no `.pier.toml`:

```bash
pier init --yes
```

`--yes` is the agent default — there's no TTY for the wizard's
prompts. The wizard inspects the compose file, fills in
`[project] / [stack] / [[expose]] / [worktree].base_ref`, sets
`match_host_uid = true` (safe for distroless / nonroot images), and
gitignores `.pier.local.toml` + `.pier/`. Re-running on an existing
manifest is safe: user-curated sections (`[env]`, `[materialize]`,
`[hooks]`, `[watch]`) pass through untouched.

**Don't hand-write `.pier.toml`** — typos in `base_domain`, missing
`match_host_uid`, slug collisions are common bugs the wizard avoids
by construction.

Common one-off flags for the agent:

- `--name <label>` — repo dir name doesn't match the desired DNS label.
- `--file path/to/compose.yml` — disambiguate when multiple compose
  files exist.
- `--service <name>` — pick which exposed service answers at
  `<slug>.<base>` (the bare alias).
- `--no-match-host-uid` — for images that hard-code their own user
  (postgres, mysql).
- `--private` — gitignore `.pier.toml` (closed-source overlay).

Full flag reference and post-init checklist (env tokens, materialize)
in [reference/init.md](reference/init.md).

## Daily commands

Run from inside the worktree you want to operate on (no flags needed in
the common case — pier resolves project + slug from the cwd).

| Task | pier command | Do NOT use |
|---|---|---|
| Start the workload | `pier up` | `docker compose up`, `make run` |
| Stop the workload | `pier down` (`--purge` to also wipe snapshots) | `docker compose down`, `docker stop` |
| Print the workload's URL | `pier url` (`--all` for every URL) | grep the manifest |
| Tail logs | `pier logs [-f] [--tail N]` | `docker compose logs` |
| Inspect containers | `pier ps` (passes through to compose) | `docker ps` (less scoped) |
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
- Resolves bare names against the effective worktree dir (see
  [reference/worktree-dir.md](reference/worktree-dir.md)).
- Forks from `manifest.worktree.base_ref` (else `main`/`master`).
- Pre-creates snapshot dirs as the host user (avoids docker creating
  them as root).
- Materializes symlinks (`.env`, `secrets/`) and snapshots
  (`data-dev/`) from the primary worktree.
- Runs `[materialize].post_create` shell commands. Failure rolls back
  the worktree (and the branch, if pier created it) unless
  `--ignore-hook-errors`.
- `--up` chains `pier up` after materialization.
- `-b <branch>`, `--from <ref>` mirror the `git worktree add` flags.

`pier worktree rm <name>`:
- Runs `[materialize].pre_remove` first, while the workload is still up
  (canonical use: `pg_dump`). Failure aborts the whole rm path unless
  `--ignore-hook-errors`.
- Then `pier down` (best-effort), unless `--skip-down` is set (use when
  the workload is already stopped — `pre_remove` still runs).
- `--purge` runs `pier down --purge` to wipe per-worktree snapshots.
- `--force` passes through to `git worktree remove --force`.

If for some reason you must use `git worktree remove` directly, run
`pier down` in the worktree FIRST, then remove. Then check `pier ls`
for any leftover state.

## Manifest essentials (`.pier.toml`)

The wizard-owned blocks are `[project]`, `[stack]`, `[[expose]]`, and
`[worktree].base_ref`. The blocks you'll edit by hand are
`[env.<service>]` and `[materialize]`.

Minimal example:

```toml
[project]
name        = "myapp"
base_domain = "myapp.{pier.tld}"     # KEEP {pier.tld}

[stack]
kind    = "compose"
file    = "docker-compose.dev.yml"
service = "front"                    # also answers at the bare <slug>.<base>

[[expose]]
service = "front"
port    = 8080

[[expose]]
service = "api"
port    = 8000

[env.front]
API_URL = "{url.api}"                # → http://api.<slug>.<base> at runtime
```

**Templating tokens** in `[env.<service>]` values: `{slug}`,
`{base_domain}`, `{pier.tld}`, `{host.<service>}`, `{url.<service>}`,
`{host.default}`, `{url.default}`. Full reference + examples in
[reference/manifest.md](reference/manifest.md).

**`[materialize]` and `[hooks]`** govern how secondary worktrees inherit
state from the primary (symlinks vs snapshots) and run shell hooks
across the worktree lifecycle (`post_create`, `pre_remove`) and the
workload lifecycle (`pre_up`, `post_up`, `pre_down`, `post_down`). All
six phases share the same `sh -c` execution model and `PIER_*` env. See
[reference/materialize.md](reference/materialize.md).

**`[stack].match_host_uid`** controls whether exposed containers run as
the host UID. `pier init` prompts for this; default is `true` (safe for
distroless / nonroot images). **`[service.<name>].match_host_uid`**
enables the same override for one compose service, including non-exposed
workers/backends. See
[reference/manifest.md](reference/manifest.md) "when to set true vs
false".

**`[[expose]].preserve_ports = [2223, ...]`** keeps selected TCP host
bindings from the compose service instead of stripping every `ports`
entry. Use only for non-HTTP protocols where traefik virtual hosts cannot
help; fixed host ports still collide between simultaneous worktrees.

**`[worktree].dir`** is a per-user preference, not a project setting.
Don't write it into `.pier.toml` proactively. See
[reference/worktree-dir.md](reference/worktree-dir.md).

## Anti-patterns to avoid

1. **Editing `.pier/compose.override.yml`.** Regenerated at every
   `pier up`. Edit `docker-compose.dev.yml` or `.pier.toml` instead.
2. **Inlining `base_domain = "myapp.test"`.** Breaks portability. Use
   `myapp.{pier.tld}`.
3. **`git worktree remove` without `pier down` first.** Leaves orphan
   containers and DNS records.
4. **Adding `default = true` to an `[[expose]]` entry.** That field
   doesn't exist. Designate the default by setting `stack.service`.
5. **Hardcoding URLs in source code.** Use env vars and let pier inject
   the right value per worktree via `[env.<service>]`.
6. **Running `docker compose up` directly.** Bypasses traefik
   registration, host port stripping, and per-worktree container
   names — multi-worktree runs will collide.
7. **Running `docker compose restart` / `stop` / `start` / `rm`
   on a pier workload.** Pier attaches the shared `pier` network
   with a unique alias *after* `compose up`. A raw compose restart
   re-creates the container without that alias, traefik loses the
   route, and short service names (`backend`, `frontend`) collide
   with other projects on the shared network. Use `pier down && pier
   up` instead, or `pier doctor --fix` to re-sync if you've already
   restarted out-of-band.
8. **Adding `[worktree].dir` to `.pier.toml` without being asked.**
   It's a per-user preference. See
   [reference/worktree-dir.md](reference/worktree-dir.md).
9. **Hand-writing `.pier.toml` from scratch.** Run `pier init --yes`
   instead. Typos in `base_domain`, missing `match_host_uid`, slug
   collisions are common bugs the wizard avoids by construction. See
   [reference/init.md](reference/init.md).

## Common one-liners

```bash
# Bootstrap pier in a fresh project (compose file present, no .pier.toml)
pier init --yes

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
  values inside browser-side bundles. Refactor to read the API URL
  from an env var, then inject via `[env.<service>]`.
- **Apps that need stable host port bindings.** pier strips host ports
  in its override to avoid multi-worktree collisions; if you must keep
  them, only one worktree at a time can run under pier.
- **Production hosting.** pier is for dev/preview only.

## Deeper references

- [reference/init.md](reference/init.md) — `pier init` flag reference,
  unattended invocation, re-init semantics, post-init checklist (env
  tokens, materialize entries to add by hand).
- [reference/manifest.md](reference/manifest.md) — full annotated
  manifest, all templating tokens, `[env.<service>]` patterns,
  `match_host_uid` decision tree, schema-only fields.
- [reference/materialize.md](reference/materialize.md) — symlinks vs
  snapshots semantics, `[materialize].post_create` / `pre_remove` and
  `[hooks].pre_up / post_up / pre_down / post_down` execution model,
  env vars, pitfalls.
- [reference/worktree-dir.md](reference/worktree-dir.md) — how the
  worktree dir is resolved across local override / manifest / prefs /
  fallback, and when (not) to commit it.
