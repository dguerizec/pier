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

## Manifest cheat sheet

```toml
[project]
name = "myapp"
base_domain = "myapp.{pier.tld}"   # KEEP {pier.tld} — never inline a literal

[stack]
kind    = "compose"
file    = "docker-compose.dev.yml"
service = "front"                  # service whose host is also the bare slug alias

[[expose]]                          # one entry per service to publish
service = "front"
port    = 8080                      # container-side port (NOT host port)

[[expose]]
service = "api"
port    = 8000
host    = "backend"                # optional; defaults to service name → backend.<slug>.<base>

[env.front]                         # injected by pier into the compose override
API_PUBLIC_URL = "{url.api}"        # → http://backend.<slug>.<base>

[materialize]
symlinks  = [".env"]
snapshots = ["data-dev/"]
```

### Templating tokens

In `env.<service>` values:

- `{slug}`, `{base_domain}`, `{pier.tld}`
- `{host.<service>}` → `<host>.<slug>.<base>` for an exposed service
- `{url.<service>}`  → `http://<host>.<slug>.<base>`
- `{host.default}` / `{url.default}` → the bare-slug alias (requires
  `stack.service` to designate an exposed service)

Manifest fields read at startup (currently `project.base_domain`)
accept `{pier.tld}` only.

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
