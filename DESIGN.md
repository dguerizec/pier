# pier — design document

> Project-agnostic CLI that gives every git worktree a stable URL on a local
> dev domain, with zero per-project DNS or proxy plumbing. Designed for the
> agentic workflow: each agent works on its own worktree, deploys to its own
> ephemeral environment, returns a clickable URL.

## 1. Goals

- **One install, all projects.** Bootstrap reverse proxy + wildcard DNS once
  per machine. Every project on the machine benefits.
- **Worktree-native.** Branch name → DNS-safe slug → URL. No manual
  `compose project name` or port juggling.
- **Stack-agnostic.** Works for `docker compose`, raw processes (`uv run`,
  `npm run dev`, `cargo run`), Dockerfile-only repos, devcontainers.
- **Headless-friendly.** Every command machine-readable, no TTY prompts in
  daily use. Wizard prompts confined to `install` and `init`.
- **Local or VPN.** Same UX whether the dev environment runs on the laptop
  itself or on a shared homelab server reached over Tailscale/WireGuard.

## 2. Non-goals

- **Not a PaaS.** No web UI, no team management, no GitHub integration, no
  preview-deploy webhook. (That's Coolify/Dokploy territory.)
- **Not a build system.** Pier orchestrates an existing dev stack; it does
  not replace cargo/npm/uv.
- **No production hosting.** Pier targets dev/preview only. Prod stays on
  whatever runner/CI you already use.
- **No multi-user authn/authz.** VPN trust is the access boundary. Optional
  basic-auth middleware is a post-MVP nice-to-have.
- **No TLS in v1.** HTTP-only on a reserved TLD. mkcert / Let's Encrypt
  comes later if needed.

## 3. User-facing surface

Three commands cover the whole UX.

### 3.1 `pier install` (once per machine)

Interactive wizard. Bootstraps the shared infra layer.

```
$ pier install
? Mode:
  ▸ local      (laptop only, traefik binds 127.0.0.1)
    server    (shared host, traefik binds 0.0.0.0)
? Base TLD: test
? Reverse proxy: traefik
? DNS resolver: dnsmasq (auto-configured)

✓ traefik container up on :80
✓ dnsmasq container up, *.test → 127.0.0.1
✓ system DNS configured (/etc/resolver/test on macOS,
  systemd-resolved per-domain on Linux)
✓ verified: curl http://anything.test → 200 (default backend)
```

Sub-flags:
- `--mode {local|server}`
- `--tld <name>` (default `test`, RFC2606 reserved)
- `--manual-dns` — skip system DNS modification, print instructions instead
- `--no-sudo` — alias of `--manual-dns`
- `--bind-ip <ip>` (server mode, default `0.0.0.0`)

`pier uninstall` reverses everything: stops containers, removes resolver
files, removes its config dir.

### 3.2 `pier init` (once per repo)

Detects the project type, generates a manifest.

```
$ cd ~/dev/myapp
$ pier init
Detected: docker-compose.dev.yml (service `app`, port 3000)
? Project name [myapp]:
? Subdomain base [myapp.test]:
? Share manifest with team (commit to git)? [y/N]:
✓ .pier.toml written
```

Detection rules (first match wins):
1. `docker-compose.dev.yml` or `docker-compose.yml` → `kind=compose`
2. `Dockerfile` only → `kind=dockerfile`
3. `package.json` with `dev` script → `kind=process` (`npm run dev`)
4. `pyproject.toml` with `[tool.uv]` → `kind=process` (`uv run …`)
5. `Cargo.toml` → `kind=process` (`cargo run`)
6. fallback → ask user

If the user answers "no" to sharing, `.pier.toml` is added to `.gitignore`.

### 3.3 Daily use

```
$ git worktree add ../myapp-feat-x -b feat/x
$ cd ../myapp-feat-x

$ pier up
✓ slug=x derived from branch feat/x
✓ materialized .env, secrets/ (symlinks from primary worktree)
✓ materialized data-dev/ (snapshot from primary worktree)
✓ container started: myapp-x
→ http://x.myapp.test

$ pier ls
PROJECT   SLUG    URL                       STATUS    UPTIME
myapp     x       http://x.myapp.test       running   2m
myapp     y       http://y.myapp.test       running   1h
otherapp  dev     http://dev.otherapp.test  running   3h

$ pier url           # current worktree URL
http://x.myapp.test

$ pier logs          # tail container/process logs

$ pier down          # stop, free the slot, keep data
$ pier down --purge  # also wipe materialized snapshots

$ pier gc            # remove orphans (worktrees deleted, containers
                     # whose branch no longer exists)

$ pier watch         # foreground: re-up on file change
                     # (opt-in, see §6.4)
```

All daily commands run from inside a worktree. Pier autodetects project
and slug from `git rev-parse`. No flags needed in the common case.

### 3.4 Client mode (multi-machine access)

For accessing a `server`-mode pier installation from a different machine:

```
$ pier client add --tld test --resolver 100.64.0.10
✓ /etc/resolver/test → 100.64.0.10  (macOS)
✓ verified: nslookup foo.test → 100.64.0.10

$ pier client tailscale
Detected tailscale, resolver suggested: 100.64.0.10
Apply split-DNS rule for `.test`? [Y/n]:
✓ tailscale set --accept-dns ... (or headscale config patch)
```

`pier client` configures the laptop. The server-mode install runs on the
remote box. The VPN must already make the resolver IP reachable; pier does
not bring its own VPN.

## 4. Architecture

Three layers, sharply separated.

```
┌──────────────────────────────────────────────────────────┐
│  CLI (pier binary)                                       │
│  install / uninstall / init / up / down / ls / url /     │
│  logs / watch / gc / client                              │
└────────────┬─────────────────────────────────────────────┘
             │ orchestrates
             ▼
┌──────────────────────────────────────────────────────────┐
│  Infra layer (bootstrapped once, lives in containers)    │
│  ├─ traefik (reverse proxy, :80, file + docker provider) │
│  └─ dnsmasq (wildcard *.<tld> → <bind-ip>)               │
│                                                          │
│  Plus host-side: /etc/resolver/<tld> or systemd-resolved │
│  per-domain rule pointing at dnsmasq.                    │
└────────────┬─────────────────────────────────────────────┘
             │ hosts
             ▼
┌──────────────────────────────────────────────────────────┐
│  Workload layer (one entry per active worktree)          │
│  ├─ compose adapter   → labels on container              │
│  ├─ process adapter   → traefik file provider entry      │
│  └─ dockerfile adapter→ wraps in temporary compose       │
└──────────────────────────────────────────────────────────┘
```

Critically: **the CLI is not a long-running process.** Once `pier up`
returns, only traefik + dnsmasq + the workload itself are running. Pier
binary can be deleted/upgraded with no impact on running envs.

### 4.1 Why this split

- **Infra layer is shared and stable.** No per-project config touches it.
  Adding a project = labeling/registering a workload, nothing more.
- **Workload layer is per-worktree and disposable.** Lifecycle bound to
  `pier up` / `pier down`. Crashes don't take infra down.
- **CLI layer is stateless** (modulo a tiny state file, see §5.3). All
  source of truth lives in: git (worktree info), docker (container state),
  filesystem (manifest, materialized files), traefik dynamic config.

## 5. Internals

### 5.1 Slug derivation

```
branch                       → slug
─────────────────────────────────────
main / master                → "dev"
feat/foo-bar                 → "foo-bar"
fix/CROPS-42                 → "crops-42"
chore/update-deps            → "update-deps"
worktree-quickfix            → "quickfix"
release/v1.2                 → "release-v1-2"
```

Algorithm:
1. Strip Conventional Branch prefix (`feat/`, `fix/`, `chore/`, `docs/`,
   `perf/`, `refactor/`, `style/`, `test/`, `ci/`, `build/`, `revert/`,
   `release/`).
2. Strip `worktree-` prefix.
3. Lowercase, replace `[A-Z_/.]` with `-`, collapse repeats.
4. Validate `^[a-z0-9][a-z0-9-]*$` (DNS label).
5. If primary worktree on `main`/`master` → force `"dev"`.

Override via `PIER_SLUG=<slug>` env or `--slug <slug>` flag.

### 5.2 Worktree detection

Pure `git rev-parse`, no fancy logic:

```
toplevel  = git rev-parse --show-toplevel       # current worktree path
gitdir    = git rev-parse --git-dir             # .git/ or .git/worktrees/<name>/
common    = git rev-parse --git-common-dir      # always primary's .git
branch    = git rev-parse --abbrev-ref HEAD
primary   = first entry of `git worktree list --porcelain`

is_primary = (gitdir == common)
project    = manifest.name  (loaded from <toplevel>/.pier.toml)
```

If no manifest is found, `pier up` errors with a hint to run `pier init`.

### 5.3 State

Pier keeps a tiny SQLite file at `~/.config/pier/state.db` with one table:

```sql
CREATE TABLE workloads (
    project       TEXT NOT NULL,
    slug          TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    branch        TEXT NOT NULL,
    kind          TEXT NOT NULL,   -- compose | process | dockerfile
    container_id  TEXT,            -- compose / dockerfile
    pid           INTEGER,         -- process kind
    port          INTEGER,         -- process kind
    started_at    INTEGER NOT NULL,
    PRIMARY KEY (project, slug)
);
```

Used by `ls`, `gc`, `down` (when invoked outside a worktree). Truth source
remains docker/process/git; the DB is a cache and is rebuildable from
inspection.

### 5.4 Manifest schema

```toml
# .pier.toml — minimal example
[project]
name = "myapp"
base_domain = "myapp.test"

[stack]
kind = "compose"             # compose | process | dockerfile
file = "docker-compose.dev.yml"
service = "app"
port = 3000

[materialize]
symlinks  = [".env", "secrets/"]
snapshots = ["data-dev/"]

[hooks]
pre_up    = "cargo build --release"   # optional shell hook
post_up   = ""
pre_down  = ""
post_down = ""

[watch]
paths     = ["src/", "Cargo.toml"]
on_change = "rebuild"        # rebuild | restart
```

Two layers, compose-style:
- `.pier.toml` — versioned (if user opted in) or gitignored. Project defaults.
- `.pier.local.toml` — always gitignored. Per-developer overrides
  (alternate ports, extra hooks, custom slug).

### 5.5 Adapters

Each adapter implements:

```
trait Adapter {
    fn up(ctx: &Ctx) -> Result<WorkloadHandle>;
    fn down(ctx: &Ctx) -> Result<()>;
    fn logs(ctx: &Ctx) -> Result<()>;
    fn status(ctx: &Ctx) -> Result<Status>;
}
```

#### compose adapter

1. Read `manifest.stack.file`.
2. Generate temporary override file `.pier/compose.override.yml`:
   ```yaml
   services:
     <service>:
       container_name: <project>-<slug>
       labels:
         - traefik.enable=true
         - traefik.http.routers.<project>-<slug>.rule=Host(`<slug>.<base_domain>`)
         - traefik.http.routers.<project>-<slug>.entrypoints=web
         - traefik.docker.network=pier
         - traefik.http.services.<project>-<slug>.loadbalancer.server.port=<port>
       networks: [default, pier]
   networks:
     pier:
       external: true
   ```
3. `docker compose -f <file> -f .pier/compose.override.yml -p <project>-<slug> up -d --build`.
4. Record `container_id` in state.

#### process adapter

1. Allocate a free TCP port (bind ephemeral, close, reuse).
2. Launch `manifest.stack.cmd` with `PORT=<port>` (or `manifest.stack.port_env`).
3. Write `~/.config/pier/traefik/dynamic/<project>-<slug>.yml`:
   ```yaml
   http:
     routers:
       <project>-<slug>:
         rule: "Host(`<slug>.<base_domain>`)"
         entryPoints: [web]
         service: <project>-<slug>
     services:
       <project>-<slug>:
         loadBalancer:
           servers:
             - url: "http://host.docker.internal:<port>"
   ```
4. Traefik file provider hot-reloads, route is live.
5. Record `pid` and `port` in state.

#### dockerfile adapter

Synthesizes a one-service compose file on the fly, then delegates to the
compose adapter. Manifest fields needed: `dockerfile` path and `port`.

### 5.6 Materialization

Run before adapter `up`. Idempotent. For each entry:
- **symlink**: if path doesn't exist in worktree, `ln -s
  <primary>/<path> <worktree>/<path>`.
- **snapshot**: if path doesn't exist in worktree, `cp -r
  <primary>/<path> <worktree>/<path>` (or `mkdir` if `--fresh`).

Skipped on the primary worktree itself.

`pier down --purge` removes the snapshot copies (not the symlinks; those
point at primary and must not be deleted).

### 5.7 Infra bootstrap (`pier install`)

Idempotent, safe to re-run.

1. Create network: `docker network create pier` (if missing).
2. Pull and run traefik:
   - `traefik:v3` (or pinned to whatever's stable at MVP time)
   - Static config: docker provider (network=pier) + file provider
     (`~/.config/pier/traefik/dynamic/`) + entrypoint web on `:80`.
   - Bind: `127.0.0.1:80` (local mode) or `0.0.0.0:80` (server mode).
   - Container name: `pier-traefik`.
3. Pull and run dnsmasq:
   - `4km3/dnsmasq` or hand-rolled minimal image.
   - Config: `address=/.test/127.0.0.1` (local) or `address=/.test/<bind-ip>` (server).
   - Listen on 53/udp and 53/tcp on `127.0.0.1` (local) or `0.0.0.0` (server).
   - Container name: `pier-dnsmasq`.
4. Configure host DNS:
   - **macOS**: write `/etc/resolver/<tld>` (sudo) with `nameserver 127.0.0.1`.
   - **Linux + systemd-resolved**: drop a unit file `/etc/systemd/resolved.conf.d/pier.conf` adding the per-domain rule, then `systemctl restart systemd-resolved`. Sudo.
   - **Linux without systemd-resolved**: detect, suggest `--manual-dns`.
5. Verify: `dig @127.0.0.1 anything.<tld>` returns the bind IP.

### 5.8 Garbage collection (`pier gc`)

Walks state DB. For each entry:
- If `worktree_path` no longer in `git worktree list` → orphan.
- If container ID no longer running and no snapshot exists → orphan.
- If branch no longer exists locally and remotely → candidate.

Prompts before removal unless `--yes`. Removes container, dynamic traefik
file, state row. Optionally also runs `git worktree remove`.

## 6. Cross-cutting concerns

### 6.1 Local vs server mode

| Concern | local mode | server mode |
|---|---|---|
| traefik bind | `127.0.0.1:80` | `0.0.0.0:80` |
| dnsmasq bind | `127.0.0.1:53` | `0.0.0.0:53` |
| dnsmasq answer | `127.0.0.1` | `<server-ip>` |
| Client config | host's own resolver | `pier client add` on each laptop |
| VPN | not involved | VPN must reach `<server-ip>` |
| Auth boundary | OS user | VPN peer set |

### 6.2 VPN integration (server mode)

Pier itself is VPN-agnostic. The server-mode install only assumes that
clients can reach `<server-ip>` somehow. Three concrete paths:

- **Tailscale**: clients run `pier client tailscale`, which sets up the
  per-domain resolver via `tailscale set --accept-dns` plus a split-DNS
  ACL on the admin side (or `extra_records` on a self-hosted headscale).
- **WireGuard plain**: clients add a search-domain + `nameserver
  <server-ip>` rule manually or via `pier client add`.
- **LAN only**: same as WireGuard, just no tunnel.

### 6.3 Authentication

VPN trust is the boundary in v1. Documented limitation: any peer in the
VPN can reach any worktree URL. Acceptable for solo + agent workflows; not
acceptable for shared dev pools.

Post-MVP escape hatch: optional `[security] basic_auth = "user:hash"` in
manifest, which generates a traefik basic-auth middleware label.

### 6.4 File watching

Two modes, both opt-in:

- **`pier watch`** — foreground command. Watches `manifest.watch.paths`,
  triggers `docker compose up -d --build` (compose) or kills+restarts the
  process (process kind). Simple `notify-rs` poll.
- **Native HMR** — for stacks with their own watcher (Vite, Next, Django
  runserver, uvicorn `--reload`, cargo-watch), pier just mounts the source
  as a volume and lets the stack handle reload. No pier intervention.

Niveau 3 Tilt-grade live update (sync-only, no rebuild) is out of scope
for v1.

### 6.5 Concurrency

- Multiple worktrees of the same project: each gets a unique slug, hence
  unique container name, traefik router, and DNS entry. No collision.
- Multiple projects: separated by `<project>` namespace in container names
  and router IDs.
- Two `pier up` invocations on the same worktree: second one is a no-op
  (container already running) or rebuilds in place (idempotent compose up).

### 6.6 Failure modes

- **traefik down**: all URLs 502. `pier doctor` (post-MVP) detects and
  offers restart.
- **dnsmasq down**: all DNS resolution for `.<tld>` fails. Same recovery.
- **Workload crashes**: traefik returns 502, container exits, state DB
  still shows it. `pier gc` cleans up.
- **State DB corrupt**: pier inspects docker/git directly to rebuild;
  state is a cache.

## 7. Tech choices

| Concern | Choice | Why |
|---|---|---|
| Language | Go | Static binary, `goreleaser` for cross-OS distribution, mature CLI ecosystem (`cobra`), good fsnotify and docker SDK. |
| Manifest format | TOML | Less indentation surprise than YAML, idiomatic for CLI tooling, comments allowed. |
| Reverse proxy | Traefik v3 | Native docker label discovery + file provider, mature. |
| DNS | dnsmasq | Single-line wildcard config, tiny, well-understood. |
| TLD default | `.test` | RFC2606 reserved, will never collide with public DNS. |
| Storage | SQLite (modernc/sqlite, no CGO) | Zero deps, file-based, easy to inspect. |
| Distribution | GitHub Releases (binaries) + Homebrew tap + curl-pipe-sh installer | Standard CLI patterns. |

## 8. Roadmap

### v0.1 — MVP

**Phase 1 — local mode end-to-end**
- CLI scaffold (cobra), command stubs.
- `install` (local mode only): traefik + dnsmasq containers, `/etc/resolver/<tld>`.
- compose adapter: override file generation, label injection.
- `init` with auto-detection for compose projects.
- `up` / `down` / `url` / `ls` / `logs`.
- State DB.
- Tested on one real compose project.

**Phase 2 — server mode + process adapter + polish**
- `install --mode server`: bind 0.0.0.0, expose dnsmasq.
- `client add`: configure remote machine resolver.
- process adapter: port allocation, traefik file provider.
- `gc`: orphan detection.
- Cross-project test (compose + uv + npm).
- README, install one-liner.

### v0.2 — short follow-ups

- `dockerfile` adapter (synthesize compose).
- `pier watch` (file watching, level 1).
- `pier doctor` (infra health check + auto-recover).
- Linux-without-systemd-resolved support (NetworkManager / dnsmasq host).
- `pier client tailscale` for one-shot tailscale split-DNS setup.
- `--shared` flag in `init` (commits manifest).

### v1 — production-ready

- TLS option (mkcert auto-trust, optional).
- Optional basic-auth middleware.
- Manifest schema validation with helpful errors.
- Telemetry (opt-in).
- Tests + CI (cross-OS).

### Post-v1 — explorations

- Live-update / sync-only file copy (Tilt-grade).
- Health-check based readiness in `up`.
- Multi-tenant on shared server (per-user namespace).
- Plugin system for custom adapters.

## 9. Open questions

1. **Manifest discovery in monorepos**: nearest `.pier.toml` walking up,
   or hard-fail at root? (Lean: nearest, with explicit `pier --root <dir>`
   override.)
2. **Slug collision across projects**: `feat/x` exists in two repos at
   once. Currently namespaced by `<project>` → no collision. URLs:
   `x.app1.test` vs `x.app2.test`. Confirm this satisfies all use cases.
3. **Snapshot vs symlink for dotfiles**: `.env` symlinked is convenient
   but can leak secrets between worktrees if any worktree mutates the
   file. Consider `mode = "ro"` for symlinks.
4. **Bring-your-own infra**: should pier accept a pre-existing traefik
   instance (e.g. user already runs one)? Probably yes via
   `install --use-existing-traefik <name>`.
5. **State DB location vs worktree**: do we need a per-worktree state
   alongside the global one? Currently no — global keyed by `(project,
   slug)` is enough.

## 10. References

- DDEV — closest comparable tool (PHP-centric historically): https://ddev.com/
- Traefik file provider: https://doc.traefik.io/traefik/providers/file/
- RFC2606 (`.test` reserved TLD): https://datatracker.ietf.org/doc/html/rfc2606
- systemd-resolved per-domain DNS: https://www.freedesktop.org/software/systemd/man/systemd-resolved.html
- Tailscale split-DNS: https://tailscale.com/kb/1054/dns
