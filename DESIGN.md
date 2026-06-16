# pier — design document

Pier is a Go CLI that gives each git worktree a stable HTTP URL on a
local or tailnet-routed dev TLD. It bootstraps a shared reverse-proxy/DNS
layer once, then each project contributes a small `.pier.toml` manifest
that tells Pier how to run the workload with Docker Compose.

This is the current design. Older sketches, including the dropped process
adapter, live in git history.

## 1. Goals

- **One install, all projects.** A machine gets one Pier infra layer:
  traefik, dnsmasq, config, state, and the optional dashboard daemon.
- **One URL per worktree.** A git branch maps to a DNS-safe slug; every
  exposed compose service gets a stable URL scoped to that slug.
- **Worktree-safe lifecycle.** `pier worktree add/rm` wraps git worktrees
  with materialization, hooks, cleanup, and optional workload start.
- **Docker-coupled execution.** Compose is the single runtime path. Even
  raw-process stacks such as uv/npm/cargo use a minimal
  `docker-compose.dev.yml`.
- **Headless daily use.** Daily commands (`up`, `down`, `url`, `logs`,
  `worktree`, API calls) are non-interactive. Prompts are limited to setup
  commands such as `install`, `init`, and `serve install`.
- **Local or tailnet.** The same project manifest works on a laptop-only
  `.test` install or a shared server reached through Tailscale/headscale.

## 2. Non-goals

- **Not a PaaS.** Pier has a local dashboard/API, but it is not a hosted
  platform, team-management layer, deployment system, or GitHub integration.
- **Not a build system.** Pier orchestrates a dev stack; the stack itself
  still owns npm/uv/cargo/go commands, hot reload, migrations, etc.
- **No production hosting.** URLs are for development and agent previews.
- **No process adapter.** Host process management was dropped to keep one
  execution model and avoid host port/PID/log semantics.
- **No multi-user authorization in v1.** The trust boundary is the local
  machine or VPN peer set. Optional basic-auth middleware is a future
  escape hatch.
- **No TLS in v1.** HTTP-only on reserved/local/tailnet names. mkcert or
  ACME can be added later if needed.

## 3. User-Facing Surface

### `pier install`

Bootstraps machine-wide infrastructure and writes Pier's install config.

The wizard inspects the host:

- Tailscale active → suggest server mode and tailnet `--answer-ip`.
- Existing dockerized traefik → BYO-traefik mode, using its docker network.
- Headscale detected → refuse Pier TLDs under `base_domain`, then offer to
  patch headscale split-DNS for a safe TLD such as `.test`.
- Otherwise → local mode on loopback with Pier-managed traefik + dnsmasq.

The install path also:

- installs the bundled AI-agent skill to `~/.agents/skills/pier`;
- links detected agent-specific skill directories when safe;
- asks for a per-user default worktree directory in `prefs.toml`.

Explicit infra flags skip wizard planning. Important flags:

- `--mode local|server`
- `--tld <name>`
- `--bind-ip <ip>`
- `--answer-ip <ip>`
- `--manual-dns` / `--no-sudo`
- `--use-existing-traefik <container>`
- `--traefik-network <network>`
- `--traefik-dynamic-dir <host-path>`
- `--yes`

`pier uninstall` removes Pier-owned infra, host DNS config, headscale split-DNS
patches, dashboard records/routes, skill installs, and optionally the binary
with `--purge`. BYO-traefik containers and networks are left alone.

### `pier serve`

`pier serve` is the optional long-running HTTP surface:

- dashboard at `/`;
- REST API under `/api/v1/`;
- SSE events for dashboard refresh;
- dashboard traefik route registration.

`pier serve install` writes a `systemctl --user` unit and starts it. The
dashboard defaults to `pier.<tld>`, which the normal Pier wildcard resolves.
If headscale `extra_records_path` exists, the user may choose a dashboard FQDN
under headscale `base_domain`:

```bash
pier serve install --dashboard-fqdn pier.nebula
```

The headscale records adapter is only for the dashboard FQDN. Workload URLs
use Pier's TLD and split-DNS route.

`pier serve upgrade` asks the running daemon to re-exec the current binary
with `SIGUSR2`. Listener file descriptors are preserved so the dashboard/API
does not drop during upgrade.

### `pier init`

Creates or updates `.pier.toml` in a project. The wizard detects Compose
files, exposed services, env interpolations, default branch, and a good
`match_host_uid` default.

Default manifest behavior:

- `.pier.toml` is versioned unless `--private` is used.
- `.pier.local.toml` and generated `.pier/` state are always gitignored.
- `base_domain` defaults to `<project>.{pier.tld}` so the same manifest works
  across contributor machines.
- `[worktree].base_ref` is recorded so `pier worktree add` forks from the
  intended branch.

Re-running `pier init` is expected. Wizard-owned fields are refreshed while
user-curated sections (`env`, `materialize`, `hooks`, `watch`) are preserved.

### Daily Worktree And Workload Commands

Use Pier's worktree wrapper instead of raw git worktree commands:

```bash
pier worktree add ../myapp-feat-x -b feat/x --up
pier url
pier logs -f
pier down
pier worktree rm ../myapp-feat-x --purge
```

`pier worktree add`:

- resolves the target under `[worktree].dir`, `prefs.toml`, or `.pier/worktrees`;
- creates or checks out the branch;
- pre-creates snapshot dirs as the host user;
- applies materialization;
- runs `[materialize].post_create`;
- rolls back the worktree and branch on failure unless hook errors are ignored;
- optionally chains `pier up`.

`pier worktree rm`:

- runs `[materialize].pre_remove`;
- runs `pier down` unless skipped;
- optionally purges snapshots;
- removes the git worktree.

`pier up/down/url/logs/ps/ls/doctor` are the core daily workload commands.
`pier gc` and `pier watch` exist as command stubs but are not implemented yet.

## 4. Architecture

Pier has four cooperating layers.

```text
CLI layer
  cobra commands, REST handlers, dashboard daemon entrypoint

Infra layer
  Pier-managed or BYO traefik, dnsmasq, host DNS config, headscale patching,
  config.toml, prefs.toml

Workload layer
  docker compose adapter, generated .pier/compose.override.yml,
  materialization, hooks, state rows

Dashboard/API layer
  pier serve, static dashboard assets, /api/v1, SSE, systemd --user unit,
  dashboard traefik route and optional headscale record
```

The CLI binary is normally short-lived. `pier serve` is the exception: it is a
long-running user service, but it is only the dashboard/API surface. Workloads,
traefik, and dnsmasq keep running without the CLI process.

## 5. Internals

### 5.1 Slug Derivation

Branch names are converted to DNS labels:

```text
branch                       slug
feat/foo-bar                 foo-bar
fix/CROPS-42                 crops-42
chore/update-deps            update-deps
worktree-quickfix            quickfix
release/v1.2                 release-v1-2
main                         main
master                       master
```

Algorithm:

1. Strip one conventional prefix:
   `feat/`, `fix/`, `chore/`, `docs/`, `perf/`, `refactor/`, `style/`,
   `test/`, `ci/`, `build/`, `revert/`.
2. Strip `worktree-`.
3. Lowercase.
4. Replace non-alphanumeric runs with `-`.
5. Trim leading/trailing `-`.
6. Validate as a DNS label.

`main` and `master` are not special-cased. Use `--slug` or `PIER_SLUG` when a
specific slug is needed.

### 5.2 Manifest

Minimal shape:

```toml
[project]
name = "myapp"
base_domain = "myapp.{pier.tld}"

[stack]
kind = "compose"
file = "docker-compose.dev.yml"
service = "web"
match_host_uid = true

[[expose]]
service = "web"
port = 3000

[materialize]
symlinks = [".env"]
snapshots = ["data-dev/"]

[worktree]
base_ref = "main"
```

Current adapter support:

- `kind = "compose"` is supported.
- `kind = "dockerfile"` is validated but rejected at runtime with a Phase 3
  hint to add a minimal compose file.
- `kind = "process"` is not supported.

Important fields:

- `[stack].service` marks the exposed service that also receives the bare
  `http://<slug>.<base_domain>` alias.
- `[[expose]].host` customizes the service subdomain; default is service name.
- `[[expose]].preserve_ports` keeps selected TCP host bindings from the
  original compose service. Use sparingly; fixed host ports can collide across
  simultaneous worktrees.
- `[stack].match_host_uid` injects `user: "<uid>:<gid>"` into exposed services.
- `[service.<name>].match_host_uid` applies the same override to any compose
  service, exposed or not.
- `[env.<service>]` values support Pier tokens such as `{slug}`,
  `{pier.tld}`, `{base_domain}`, `{host.<service>}`, and `{url.<service>}`.
- `[hooks]` covers `pre_up`, `post_up`, `pre_down`, `post_down`.
- `[materialize]` covers `post_create` and `pre_remove` worktree hooks.
- `[watch]` parses and is preserved, but `pier watch` is not implemented.

### 5.3 Compose Adapter

The compose adapter is the single workload runtime path.

At `pier up`:

1. Resolve worktree, manifest, slug, install config, and state DB.
2. Run `pre_up`.
3. Apply symlinks/snapshots from the primary worktree.
4. Generate `.pier/compose.override.yml`.
5. Run `docker compose -f <file> -f .pier/compose.override.yml -p <project>-<slug> up -d --build`.
6. Connect exposed containers to the traefik discovery network with
   worktree-scoped aliases.
7. Persist the workload row in state.
8. Run `post_up`.
9. Print URLs.

The generated override owns:

- container names scoped by project, slug, and service;
- traefik labels for each exposed service;
- the bare-slug alias for `[stack].service`;
- port stripping, except selected `preserve_ports`;
- `user: "<uid>:<gid>"` where requested;
- templated environment variables.

Do not edit `.pier/compose.override.yml`; it is regenerated.

### 5.4 DNS And Routing

Pier workload URLs are always under the Pier TLD after token expansion:

```text
http://<slug>.<project>.<tld>
http://<service>.<slug>.<project>.<tld>
```

Local mode:

- traefik listens on loopback;
- dnsmasq answers `*.<tld>` with `127.0.0.1`;
- Linux systemd-resolved gets a per-domain drop-in.

Server mode:

- traefik listens on the chosen bind IP;
- dnsmasq answers `*.<tld>` with `answer_ip`;
- peers use split-DNS to send `.<tld>` queries to the Pier server.

Headscale:

- Pier refuses a workload TLD under headscale `base_domain` because MagicDNS
  owns that zone and preempts split-DNS routing.
- For safe TLDs outside `base_domain`, `pier install` can patch
  `dns.nameservers.split.<tld>` in headscale config and restart headscale.
- `extra_records_path` is only used for dashboard FQDNs under
  `base_domain`, configured by `pier serve install`.

Use `resolvectl query` when validating Linux resolution. `dig` can bypass
systemd-resolved per-link routing and produce false negatives.

### 5.5 State And Config Files

Pier stores machine state under the user config directory:

```text
~/.config/pier/config.toml   install config
~/.config/pier/prefs.toml    user workflow preferences
~/.config/pier/state.db      SQLite cache
~/.config/pier/traefik/      Pier-managed traefik config
```

`config.toml` includes mode, TLD, bind/answer IPs, BYO-traefik settings,
headscale config paths, and dashboard FQDN state.

`prefs.toml` currently holds the default worktree directory.

`state.db` tracks registered projects and running workloads. Workload state is
a cache; docker, git, and filesystem reality win. `doctor --fix` can drop dead
rows. `gc` is reserved for broader orphan cleanup but is not implemented yet.

### 5.6 Materialization And Hooks

Materialization exists so secondary worktrees inherit enough local state to run.

- `symlinks`: link from primary into secondary if missing.
- `snapshots`: copy from primary into secondary if missing.
- Empty destination snapshot directories are removed before copy so Docker
  does not leave root-owned empty dirs that block writes.
- `pier down --purge` removes snapshots, not symlinks.

Hooks run through `sh -c` with a stable set of `PIER_*` environment variables.
They are used for migrations, seed data, DB dumps, and per-worktree setup.

### 5.7 Dashboard/API

The REST API drives the same code paths as the CLI:

- list workloads and projects;
- run up/down;
- stream logs;
- create/delete worktrees;
- run doctor;
- stream state events.

The dashboard is static HTML/CSS/JS embedded into the binary. It is intentionally
small: no build step, no frontend package manager, no external runtime.

`pier serve` binds to enough addresses for both browser access and traefik
access:

- loopback;
- the Pier docker bridge gateway when available;
- `answer_ip` in server mode when needed.

In BYO-traefik mode, `pier serve` writes `pier-dashboard.yml` into the detected
or configured file-provider directory. In Pier-managed mode it writes the same
route into Pier's dynamic config directory.

## 6. Operational Invariants

- Do not run raw `docker compose up/down/restart` for Pier workloads. Pier must
  regenerate overrides, attach the shared traefik network, and update state.
- Do not remove worktrees with raw `git worktree remove` unless `pier down`
  has already run. Prefer `pier worktree rm`.
- Keep `{pier.tld}` in committed manifests unless a project intentionally needs
  a fixed domain.
- Keep runtime-generated `.pier/` files out of git.
- Trust internal callers; validate user input and external system state at the
  boundary.
- Keep CLI code thin. Complex behavior belongs in `internal/<package>` with
  unit tests where I/O can be faked.

## 7. Known Platform Constraints

- Linux is the only platform with automatic host DNS setup today. Non-Linux
  builds can print manual guidance but do not mutate host DNS.
- Docker is required. Pier does not manage host processes.
- Server mode assumes peers can reach `answer_ip` over LAN/VPN.
- Fixed preserved host ports collide across worktrees; they are an escape hatch
  for non-HTTP protocols.
- Containers with non-root default users often need `match_host_uid = true` on
  bind-mounted host paths.
- dnsmasq in container networking can drop UDP replies on some Linux kernels;
  Pier runs its dnsmasq container on host networking.
- dnsmasq must not use `bind-interfaces` with `--listen-address=0.0.0.0`.

## 8. Roadmap

Implemented:

- Compose adapter.
- Local and server installs.
- BYO-traefik.
- Headscale split-DNS patch/unpatch.
- Dashboard/API daemon with systemd --user install and upgrade.
- Dashboard FQDN via headscale `extra_records_path`.
- Worktree add/rm/clean wrappers.
- Materialization and hooks.
- AI-agent skill installation.
- Doctor checks and selected fix paths.
- Curl-pipe installer and GoReleaser config.

Not implemented yet:

- `pier gc` beyond the command stub.
- `pier watch` beyond the command stub.
- Dockerfile adapter.
- TLS.
- Optional basic auth.
- Automatic host DNS setup outside Linux.
- Health-check-based workload readiness.

Open design questions:

- How far the dashboard/API should go before it becomes a real control plane.
- Whether monorepo support should walk up to the nearest `.pier.toml` or require
  explicit project registration.
- Whether preserved TCP ports need per-user allocation helpers.
- Whether future Dockerfile support should synthesize compose or only generate
  a starter compose file.

## 9. References

- README.md — user-facing usage.
- AGENTS.md — contributor and agent workflow constraints.
- RFC2606 (`.test` reserved TLD): https://datatracker.ietf.org/doc/html/rfc2606
- systemd-resolved per-domain DNS: https://www.freedesktop.org/software/systemd/man/systemd-resolved.html
- Tailscale split-DNS: https://tailscale.com/kb/1054/dns
