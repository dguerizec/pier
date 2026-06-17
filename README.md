# pier

> One CLI, one URL per git worktree. No per-project DNS or proxy plumbing.

Pier gives every git worktree a stable URL on a local dev TLD. Bootstrap traefik + dnsmasq + host DNS once, then `pier up` per worktree returns a clickable URL. Designed for the agentic workflow: each agent works on its own worktree, deploys to its own ephemeral env, returns a URL.

```bash
$ pier worktree add ../myapp-feat-x -b feat/x
$ cd ../myapp-feat-x
$ pier up
→ http://feat-x.myapp.test
```

Architecture and roadmap live in [DESIGN.md](DESIGN.md). This README is the practical "how do I use it" guide.

## Status

Phase 1 MVP and most of Phase 2 are shipped. Compose adapter, install wizard, BYO-traefik, server mode, headscale split-DNS patching, dashboard/API server, doctor, materialize, worktree wrapper, and AI agent skill install — all in. Backlog: MCP shim, dockerfile adapter (synthesized compose), gc, watch, macOS DNS support. See [DESIGN.md §8](DESIGN.md#8-roadmap).

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

The wizard inspects the host and proposes a single concrete plan:

- **Tailscale detected?** Server mode, `--bind-ip` from your tailnet IP.
- **Existing traefik container?** BYO-traefik mode — pier registers workloads on it instead of spawning its own.
- **Headscale detected?** pier refuses TLDs under `base_domain`, then can patch split-DNS for a safe TLD such as `.test`.
- **Otherwise** — local mode, traefik + dnsmasq under `~/.config/pier/`, systemd-resolved drop-in for `.test`.

Output looks like this:

```
$ pier install
Detected:
  ✓ tailscale: 100.64.0.10 on my-tailnet
  ✓ existing traefik: container=traefik network=proxy
  ✓ headscale: container=headscale base_domain=nebula records=/etc/headscale/dns_records.json

Plan:
  --mode server --tld test --bind-ip 100.64.0.10 --answer-ip 100.64.0.10 --use-existing-traefik traefik --traefik-network proxy

Apply this plan? [Y/n]
```

Pass `-y` to accept silently (CI / agent-friendly). Pass any explicit infra flag (`--mode`, `--tld`, ...) to skip the wizard. The installer also writes the bundled AI-agent skill to `~/.agents/skills/pier` and, when run interactively, asks for your default `pier worktree add <name>` directory.

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

Pier has two workload routing modes, picked automatically by the wizard:

| Mode | When | URL example | DNS routing |
|---|---|---|---|
| **local** | no tailscale, no headscale | `feat-x.myapp.test` | pier-dnsmasq on `127.0.0.1`, systemd-resolved drop-in |
| **server + split-DNS** | tailscale + headscale, TLD outside base_domain | `feat-x.myapp.test` | pier-dnsmasq on tailnet IP, headscale `dns.nameservers.split` |

When a tailnet already uses Headscale `extra_records_path` for prod hostnames, Pier can use that records adapter for the dashboard FQDN. It does not use records for per-worktree workload URLs.

## Health & recovery

```bash
pier doctor             # diagnose infra + state
pier doctor --fix       # restart down containers, prune dead workload rows
```

`doctor` adapts to the active mode: it skips pier-traefik checks in BYO mode, warns about stale workload rows, and reports legacy system-level `pier.service` units left behind by older installs.

## Multi-machine (tailnet) access

When pier runs on a tailnet host in server mode, peers can reach the URLs through the same tailnet:

```bash
# on a peer machine
pier client tailscale     # prints exact split-DNS / extra_records snippets
                          # for both Tailscale.com and headscale config.yaml
```

The install wizard can auto-apply the Headscale split-DNS rule when the TLD is outside `base_domain`. `extra_records_path` is only needed when you choose a dashboard FQDN under the Headscale `base_domain`.

Test peer resolution with `resolvectl query <slug>.<base_domain>` rather than `dig`. Dig may bypass systemd-resolved per-link routing on Linux and produce false negatives.

## Caveats

- **Linux only** for host DNS auto-config in MVP. macOS support is on the v0.2 list.
- **No TLS** — HTTP only on the reserved `.test` TLD, or for the dashboard under your tailnet base domain when configured. mkcert + Let's Encrypt is post-v1.
- **Trust boundary = VPN peers**. Anyone in your tailnet can reach any pier URL. A `[security].basic_auth` middleware is a post-MVP nice-to-have.
- **Compose only.** Even raw-process stacks (uv/npm/cargo) declare a `docker-compose.dev.yml` — see the minimal snippet below. The dockerfile adapter (which synthesizes a compose file from a Dockerfile) lands in Phase 3.

## Contributing

Pier was originally created by [@LeoPartt](https://github.com/LeoPartt).

Pier is built around a sharp layer separation (CLI / infra / workload / dashboard — see DESIGN.md §4). Adding a new adapter is `internal/adapter/<kind>.go` implementing the adapter interface. Adding a new infra component goes in `internal/infra/`. The CLI surface in `internal/cli/` should stay a thin shim over those packages.

Run `go test ./...`. I/O-heavy paths in `infra` and `cli` have targeted unit tests, but still need smoke testing on real Linux hosts.

## License

MIT. See [LICENSE](LICENSE).
