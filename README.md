# pier

> One CLI, one URL per git worktree. No per-project DNS or proxy plumbing.

Pier gives every git worktree a stable URL on a local dev TLD. Bootstrap traefik + dnsmasq + host DNS once, then `pier up` per worktree returns a clickable URL. Designed for the agentic workflow: each agent works on its own worktree, deploys to its own ephemeral env, returns a URL.

```bash
$ git worktree add ../myapp-feat-x -b feat/x
$ cd ../myapp-feat-x
$ pier up
→ http://feat-x.myapp.test
```

Architecture and roadmap live in [DESIGN.md](DESIGN.md). This README is the practical "how do I use it" guide.

## Status

Phase 1 MVP and most of Phase 2 are shipped. Compose adapter, install wizard, BYO-traefik, server mode, headscale records mode, doctor, materialize, worktree wrapper — all in. Backlog: MCP shim, dockerfile adapter (synthesized compose), gc, watch, macOS DNS support. See [DESIGN.md §8](DESIGN.md#8-roadmap).

Pier is intentionally **docker-coupled** — even projects that aren't otherwise containerized declare a minimal `docker-compose.dev.yml`. See the snippet in [Per-repo setup](#per-repo-setup-once-per-project) below.

## Install

Build from source — pier is a single static binary.

```bash
git clone https://github.com/LeoPartt/pier.git
cd pier
go build -o ~/.local/bin/pier ./cmd/pier
pier --version
```

Go 1.23+ recommended. `goreleaser` cross-platform builds + homebrew tap will follow once Phase 2 is fully merged to main.

## Bootstrap (once per machine)

```bash
pier install
```

The wizard inspects the host and proposes a single concrete plan:

- **Tailscale detected?** Server mode, `--bind-ip` from your tailnet IP.
- **Existing traefik container?** BYO-traefik mode — pier registers workloads on it instead of spawning its own.
- **Headscale running with `extra_records_path`?** Records mode — pier publishes per-slug A records via MagicDNS, no dnsmasq needed.
- **Otherwise** — local mode, traefik + dnsmasq under `~/.config/pier/`, systemd-resolved drop-in for `.test`.

Output looks like this:

```
$ pier install
Detected:
  ✓ tailscale: 100.64.0.10 on my-tailnet
  ✓ existing traefik: container=traefik network=proxy
  ✓ headscale: container=headscale base_domain=nebula records=/etc/headscale/dns_records.json

Plan:
  --mode server --tld nebula --bind-ip 100.64.0.10 --use-existing-traefik traefik --traefik-network proxy (records mode: ...)

Apply this plan? [Y/n]
```

Pass `-y` to accept silently (CI / agent-friendly). Pass any explicit infra flag (`--mode`, `--tld`, ...) to skip the wizard.

`pier uninstall` reverses everything (containers, network, host DNS drop-in, config dir). BYO mode leaves the user's traefik + network alone.

## Per-repo setup (once per project)

```bash
$ cd ~/dev/myapp
$ pier init
Detected: docker-compose.dev.yml
? Project name [myapp]:
? Base domain [myapp.test]:
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
base_domain = "myapp.test"

[stack]
kind            = "compose"             # compose only today
file            = "docker-compose.dev.yml"
service         = "web"
port            = 3000
match_host_uid  = true                  # opt-in: container runs as host UID/GID
                                        #   (resolves EACCES on bind-mounts when
                                        #    the image uses distroless/nonroot)

[materialize]
symlinks  = [".env", "secrets/"]        # symlinked from primary on first up
snapshots = ["data-dev/"]               # copied per worktree (own mutable copy)
```

`.pier.local.toml` next to it is always gitignored — per-developer overrides (alternate ports, custom slug, etc.).

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

Pier has three operating modes, picked automatically by the wizard:

| Mode | When | URL example | DNS routing |
|---|---|---|---|
| **local** | no tailscale, no headscale | `feat-x.myapp.test` | pier-dnsmasq on `127.0.0.1`, systemd-resolved drop-in |
| **server + split-DNS** | tailscale + headscale, TLD outside base_domain | `feat-x.myapp.test` | pier-dnsmasq on tailnet IP, headscale `dns.nameservers.split` |
| **server + records** | tailscale + headscale + `extra_records_path` configured | `feat-x.myapp.nebula` | pier writes per-slug records to `dns_records.json`, MagicDNS resolves |

Records mode is the cleanest for a tailnet that already uses `extra_records_path` for prod hostnames — no dnsmasq, no host DNS drop-in, no headscale.yaml patch. `pier up` writes one record, `pier down` removes it, file-locked so concurrent agents serialize cleanly.

## Health & recovery

```bash
pier doctor             # diagnose infra + state
pier doctor --fix       # restart down containers, prune dead workload rows
```

`doctor` adapts to the active mode: it skips dnsmasq checks in records mode, skips pier-traefik checks in BYO mode, etc.

## Multi-machine (tailnet) access

When pier runs on a tailnet host in server mode, peers can reach the URLs through the same tailnet:

```bash
# on a peer machine
pier client tailscale     # prints exact split-DNS / extra_records snippets
                          # for both Tailscale.com and headscale config.yaml
```

The install wizard auto-applies the headscale split-DNS rule when records mode is not in play and the TLD is outside the base_domain. For records mode, no peer config is needed — MagicDNS distributes records directly.

Test peer resolution with `resolvectl query <slug>.<base_domain>` rather than `dig`. Dig may bypass systemd-resolved per-link routing on Linux and produce false negatives.

## Caveats

- **Linux only** for host DNS auto-config in MVP. macOS support is on the v0.2 list.
- **No TLS** — HTTP only on the reserved `.test` TLD (or under your tailnet base_domain in records mode). mkcert + Let's Encrypt is post-v1.
- **Trust boundary = VPN peers**. Anyone in your tailnet can reach any pier URL. A `[security].basic_auth` middleware is a post-MVP nice-to-have.
- **Compose only.** Even raw-process stacks (uv/npm/cargo) declare a `docker-compose.dev.yml` — see the minimal snippet below. The dockerfile adapter (which synthesizes a compose file from a Dockerfile) lands in Phase 3.

## Contributing

Pier is built around a sharp three-layer separation (CLI / infra / workload — see DESIGN.md §4). Adding a new adapter is `internal/adapter/<kind>.go` implementing the four-method interface. Adding a new infra component goes in `internal/infra/`. The CLI surface in `internal/cli/` should stay a thin shim over those packages.

Run `go test ./...` — every package has tests except the I/O-heavy ones (`infra`, `cli`), which are validated through the smoke-test workflow on real hosts.

## License

TBD — pinning a license is on the pre-1.0 todo list.
