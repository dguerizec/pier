# pier

<p align="center">
  <img src="assets/pier.png" alt="Pier" width="480">
</p>

> One CLI, one URL per git worktree. No per-project DNS or proxy plumbing.

Pier gives every git worktree a stable URL on a local dev TLD. Bootstrap traefik + dnsmasq + host DNS once, then `pier up` per worktree returns a clickable URL. Designed for the agentic workflow: each agent works on its own worktree, deploys to its own ephemeral env, returns a URL.

**Pier is local by default.** It requires Docker, but it does **not** require
Tailscale or Headscale. LAN and Tailscale access are optional choices offered
during installation; the standard local installation binds Pier to the
development machine.

```bash
$ pier worktree add ../myapp-feat-x -b feat/x
$ cd ../myapp-feat-x
$ pier up
→ http://feat-x.myapp.test
```

Architecture and roadmap live in [DESIGN.md](DESIGN.md). This README is the practical "how do I use it" guide.

## Status

Phase 1 MVP and most of Phase 2 are shipped. Compose adapter, local-first install wizard, BYO-traefik, optional LAN/Tailscale access, selective LAN sharing, optional Headscale split-DNS patching, dashboard/API server, doctor, materialize, worktree wrapper, and AI agent skill install — all in. Backlog: MCP shim, dockerfile adapter (synthesized compose), gc, watch, macOS DNS support. See [DESIGN.md §8](DESIGN.md#8-roadmap).

Pier is intentionally **docker-coupled** — even projects that aren't otherwise containerized declare a minimal `docker-compose.dev.yml`. See the snippet in [Per-repo setup](#per-repo-setup-once-per-project) below.

## Install

### One-liner (Linux, macOS — amd64 or arm64)

```bash
curl -fsSL https://raw.githubusercontent.com/dguerizec/pier/main/install.sh | sh
```

The script picks the right archive from the latest GitHub release, verifies its
sha256 against the published `checksums.txt`, and installs into `~/.local/bin`
(or falls back to `/usr/local/bin` via sudo). Set `PIER_VERSION=v0.x.y` to pin a
specific release, or `PIER_INSTALL_DIR=/some/path` to override the destination.

Audit the script before piping it to a shell — it's a plain POSIX shell script
in this repo at [`install.sh`](install.sh).

### From source

```bash
git clone https://github.com/dguerizec/pier.git
cd pier
go build -o ~/.local/bin/pier ./cmd/pier
pier --version
```

Go 1.26+ required. Homebrew tap (`brew install dguerizec/pier/pier`) will follow.

## Bootstrap (once per machine)

```bash
pier install
```

The wizard starts with the safe local-only choice:

```text
URL reachability
> Local only (recommended) — reachable from this machine
  LAN (optional) — reachable from devices on your local network
  Tailscale (optional, when detected) — reachable from your tailnet
```

LAN is always offered. The Tailscale choice appears only when an active
Tailscale IPv4 is detected. Headscale integration is offered later only when
Headscale is present and Tailscale access was selected.

The wizard also detects an existing dockerized Traefik and can use its network
instead of spawning `pier-traefik`. With the default local choice, Pier runs
Traefik + dnsmasq on loopback and installs a systemd-resolved route for `.test`.

Output looks like this:

```text
$ pier install
Detected:
  no optional integrations detected; local mode is ready

Plan:
  --mode local --tld test

Apply this plan? [Y/n]
```

Pass `-y` to accept the local default silently (CI / agent-friendly). Explicit
install-shape flags such as `--mode`, `--bind-ip`, and `--answer-ip` skip the
wizard; `--tld` can customize either path. The installer also writes the
bundled AI-agent skill to `~/.agents/skills/pier` and, when run interactively,
asks for your default `pier worktree add <name>` directory.

`pier uninstall` reverses everything (containers, network, host DNS drop-in, config dir). BYO mode leaves the user's traefik + network alone. The pier binary itself stays in place — pass `--purge` to also delete it (`pier uninstall --purge`). `--purge` declines when the binary lives under a brew prefix or system path; let the package manager remove it in that case.

## Dashboard / API

```bash
pier serve install
```

`pier serve` exposes the dashboard at `/` and the REST API at `/api/v1/`. `pier serve install` installs it as a `systemctl --user` unit and publishes the dashboard at `pier.<tld>` by default, which is covered by the same split-DNS wildcard as workloads.

If Headscale has `extra_records_path` configured, `pier serve install` can place the dashboard under the Headscale `base_domain` instead:

```bash
pier serve install --dashboard-fqdn pier.nebula
```

That records adapter is for the dashboard hostname only. Workload URLs still use Pier's TLD and split-DNS route.

## Per-repo setup (once per project)

```bash
$ cd ~/dev/myapp
$ pier init
Detected: docker-compose.dev.yml
? Project name [myapp]:
? Base domain [myapp.{pier.tld}]:
? Compose service: web
? Service port: 3000
? Share manifest with team (commit to git)? [Y/n]:
✓ .pier.toml written
```

Defaults to committing the manifest so secondary worktrees get it for free via `git checkout`. Pass `--private` to gitignore it instead.

`pier init` is non-interactive when you pass the fields:

```bash
pier init -y --service web --port 3000
```

### Manifest reference

```toml
[project]
name        = "myapp"
base_domain = "myapp.{pier.tld}"

[stack]
kind            = "compose"             # compose only today
file            = "docker-compose.dev.yml"
service         = "web"
port            = 3000
match_host_uid  = true                  # opt-in: container runs as host UID/GID
                                        #   (resolves EACCES on bind-mounts when
                                        #    the image uses distroless/nonroot)
                                        #   applies to every exposed service

[[expose]]
service = "web"
port = 3000
preserve_ports = [2223]                 # optional: keep selected TCP host
                                        # bindings from compose (for SSH,
                                        # databases, or other non-HTTP TCP)

[service.worker]
match_host_uid = true                   # same override for one compose service,
                                        # exposed or not

[materialize]
symlinks  = [".env", "secrets/"]        # symlinked from primary on first up
snapshots = ["data-dev/"]               # copied per worktree (own mutable copy)
```

`.pier.local.toml` next to it is always gitignored — per-developer overrides (custom slug, worktree dir, etc.).

`preserve_ports` keeps a matching Compose `ports:` entry for protocols that cannot
go through Traefik's HTTP routing. It does not allocate a different host port by
itself; make the Compose published port configurable when multiple worktrees must
run at once:

```yaml
services:
  web:
    ports:
      - "${SSH_HOST_PORT:-2223}:2223"
```

Then set `SSH_HOST_PORT=2224` in that worktree's local `.env`. The manifest can
stay shared as `preserve_ports = [2223]` because pier matches either side of the
Compose binding and keeps the resolved `2224:2223` entry.

For values that must be computed per worktree, `hooks.resolve_values` prints a
JSON object before pier performs the final manifest parse:

```toml
[stack.env]
PICKATUBE_OAUTH_RELAY_PORT = "{value.oauth_callback_port}"

[[expose]]
service = "web"
port = 3000
preserve_ports = [{value.oauth_callback_port}]

[env.web]
OAUTH_CALLBACK_URL = "http://127.0.0.1:{value.oauth_callback_port}/callback"

[hooks]
resolve_values = "./scripts/resolve-pier-values"
```

`[stack.env]` maps the resolved value to a project-owned variable passed to
Docker Compose. The source Compose file can remain Pier-agnostic and retain its
vanilla default:

```yaml
services:
  web:
    ports:
      - "${PICKATUBE_OAUTH_RELAY_PORT:-8765}:8765"
```

This mechanism is not port-specific: `[stack.env]` participates in normal
Compose interpolation anywhere in the source model, while `[env.<service>]`
injects variables into a container. Each returned scalar is still exported as
`PIER_VALUE_<UPPERCASE_NAME>` for hooks and direct Compose use. Pier caches the
resolved object in the worktree's `.pier/resolved-values.json`; the hook owns
allocation policy and collision handling.

### Minimal compose for raw-process stacks

Pier requires a `docker-compose.dev.yml` even when your project isn't otherwise containerized — same execution path on every host, no host port/PID/log juggling. For Python / Node / Rust projects the file is ~10 lines:

```yaml
# docker-compose.dev.yml
services:
  app:
    image: python:3.13-slim                 # or node:20, rust:1, etc.
    working_dir: /app
    volumes:
      - ./:/app
    command: sh -c "pip install uv && uv sync && uv run python run.py"
    ports:
      - "${PORT:-3000}:3000"
```

Adjust the image, command, and port for your stack. `pier init` then detects it like any other compose file.

## Daily workflow

```bash
# spawn an isolated environment for a feature branch
pier worktree add ../myapp-feat-x -b feat/x
cd ../myapp-feat-x

# materialize already ran via worktree add; just bring it up
pier up
→ http://feat-x.myapp.test

# inspect
pier ls
pier url                       # current worktree URL
pier logs -f                   # tail logs

# tear down
pier down                      # stop, keep snapshots
pier down --purge              # also wipe snapshot copies (data-dev/)

# clean cleanup
pier worktree rm ../myapp-feat-x --purge
```

Slug is derived from the branch name (DESIGN §5.1): `feat/foo-bar` → `foo-bar`, `main` → `main`. Override with `--slug` or `PIER_SLUG=...`.

## Modes

Pier is local unless the user explicitly chooses a broader reach:

| Wizard choice | Required software/network | URL example | DNS routing |
|---|---|---|---|
| **Local only (default)** | Docker | `feat-x.myapp.test` | pier-dnsmasq on `127.0.0.1`, local systemd-resolved route |
| **LAN (optional)** | A trusted LAN with an assigned IPv4 | `feat-x.myapp.test` | pier-dnsmasq on the chosen LAN IP; clients route `.test` to that IP |
| **Tailscale (optional)** | Active Tailscale client | `feat-x.myapp.test` | pier-dnsmasq on the detected Tailscale IP; tailnet split-DNS |

Headscale is never required. When detected for the selected Tailscale option,
Pier can patch its split-DNS configuration. If that Headscale instance already
uses `extra_records_path`, Pier can also use it for an optional dashboard FQDN;
workload URLs do not use that records adapter.

## Health & recovery

```bash
pier doctor             # diagnose infra + state
pier doctor --fix       # restart down containers, prune dead workload rows
```

`doctor` adapts to the active mode: it skips pier-traefik checks in BYO mode, warns about stale workload rows, and reports legacy system-level `pier.service` units left behind by older installs.

## Multi-machine access

For a LAN, select the LAN option during `pier install`, then route the Pier TLD
on each client to the chosen server address:

```bash
pier client add --tld test --resolver 192.168.1.42
```

For Tailscale, select the Tailscale option when it is offered. Tailnet peers
can then reach Pier through split-DNS:

```bash
# on a peer machine
pier client tailscale     # prints exact split-DNS / extra_records snippets
                          # for both Tailscale.com and headscale config.yaml
```

If Headscale is detected, the wizard can auto-apply its split-DNS rule when the
TLD is outside `base_domain`. `extra_records_path` is only needed when you
choose a dashboard FQDN under the Headscale `base_domain`.

Test peer resolution with `resolvectl query <slug>.<base_domain>` rather than `dig`. Dig may bypass systemd-resolved per-link routing on Linux and produce false negatives.

## Selective LAN sharing

Pier installs can publish individual workload hosts on a LAN without exposing
Pier's wildcard DNS or every active worktree. This is the recommended approach
when a local-first install only needs to share a few URLs. Server mode also
works when its main proxy is bound to a distinct, specific address rather than
`0.0.0.0`:

```bash
cd /path/to/jobo
pier share add backend
pier share add '*' --persist
```

With no host or address flags, `share add` interactively asks which exact hosts
to publish and which assigned LAN address to bind. In scripts, pass
`--interface enp3s0` or `--bind-ip 192.168.1.42`. Shell globs must be quoted.
They are expanded once against the current worktree's known URLs; Pier writes
exact Traefik `Host` rules, so a later `admin` service is not silently shared.

Session shares survive `pier down` / `pier up` but disappear when their
dedicated LAN gateway restarts. `--persist` saves the route and gives the
gateway an `unless-stopped` restart policy so it returns after a machine
restart. This persists the route, not the Compose workload: a stopped workload
returns an unavailable response until `pier up` (or its own Compose restart
policy) brings it back.

```bash
pier share list
pier share hosts               # paste-ready /etc/hosts lines
pier share url --default       # exactly one entry-point URL
pier share url --all
pier share remove backend
```

The client needs no router or DNS change. Copy the `pier share hosts` output
into its `/etc/hosts`; the Pier host firewall must allow inbound TCP/80 on the
selected interface. Sharing is hostname-selective, not user authentication:
any LAN peer that knows a shared hostname can request it.

## Caveats

- **Linux only** for host DNS auto-config in MVP. macOS support is on the v0.2 list.
- **No TLS** — HTTP only on the reserved `.test` TLD, or for the dashboard under an optional tailnet base domain when configured. mkcert + Let's Encrypt is post-v1.
- **Local by default; trusted networks only when shared.** LAN or Tailscale server mode exposes every Pier URL to peers that can reach the selected address. Use `pier share` for a finite LAN allowlist. A `[security].basic_auth` middleware is a post-MVP nice-to-have.
- **Compose only.** Even raw-process stacks (uv/npm/cargo) declare a `docker-compose.dev.yml` — see the minimal snippet below. The dockerfile adapter (which synthesizes a compose file from a Dockerfile) lands in Phase 3.

## Contributing

Pier was originally created by [@LeoPartt](https://github.com/LeoPartt).

Pier is built around a sharp layer separation (CLI / infra / workload / dashboard — see DESIGN.md §4). Adding a new adapter is `internal/adapter/<kind>.go` implementing the adapter interface. Adding a new infra component goes in `internal/infra/`. The CLI surface in `internal/cli/` should stay a thin shim over those packages.

Run `go test ./...`. I/O-heavy paths in `infra` and `cli` have targeted unit tests, but still need smoke testing on real Linux hosts.

## License

MIT. See [LICENSE](LICENSE).
