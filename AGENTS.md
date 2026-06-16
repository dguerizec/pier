# AGENTS.md

Context for AI coding agents working on this repository. Follows the [AGENTS.md](https://agents.md/) format — vendor-neutral, agent-agnostic. Tool-specific entry points (e.g. `CLAUDE.md`) link here.

## What pier is

A Go CLI that gives every git worktree a stable URL on a local dev TLD by orchestrating traefik + dnsmasq, with split-DNS support for tailnet access. Read [DESIGN.md](DESIGN.md) before any non-trivial change — it pins architecture, the layer separation, and explicit non-goals. The [README.md](README.md) covers user-facing behaviour.

## Where things live

```
cmd/pier/main.go        entry point — just calls cli.Execute()
internal/cli/           cobra commands + the REST API on `pier serve` (api*.go). Each command is a thin shim over internal/<package>.
internal/adapter/       compose adapter (today); dockerfile (synthesized compose) lands in Phase 3. Pier is intentionally docker-coupled — no process adapter.
internal/infra/         install/uninstall/doctor: traefik + dnsmasq + host DNS bootstrap; per-user prefs.toml.
internal/initwizard/    `pier init` logic: detect compose services, scan env interpolations, plan/prompt/apply manifest changes. Pure helpers, charm/huh prompts, slug + tld + tty + gitignore utilities.
internal/detect/        host introspection (tailscale, traefik, headscale) for the install wizard.
internal/headscale/     split-DNS yaml patch + extra_records JSON adapter (file-locked).
internal/manifest/      .pier.toml parsing/validation (BurntSushi/toml).
internal/materialize/   symlink + snapshot copy from primary to secondary worktrees.
internal/slug/          branch-name → DNS-safe slug.
internal/state/         SQLite cache of running workloads (modernc.org/sqlite, no CGO).
internal/worktree/      git worktree metadata via plumbing commands.
```

The CLI layer is intentionally thin: complex logic belongs in `internal/<package>` and gets unit-tested there. `internal/cli/` mostly resolves the `daily` context and dispatches.

## Build, test, run

```bash
go build ./...                          # whole tree builds
go test ./...                           # unit tests
go build -o ~/.local/bin/pier ./cmd/pier   # install locally
go vet ./...
```

There is no formal lint config yet — vet + go fmt + the existing test suite are the gates.

I/O-heavy `internal/infra` and `internal/cli` paths have targeted unit tests, but they still need smoke tests on real Linux hosts (the user runs them in a terminal). Don't pretend that test gaps mean "nothing to validate" — write tests where I/O can be faked, smoke-test the rest with the user.

## Dependencies

Locked-in: `github.com/spf13/cobra`, `github.com/BurntSushi/toml`, `gopkg.in/yaml.v3`, `modernc.org/sqlite`, `github.com/charmbracelet/huh` (interactive forms in `internal/initwizard/` and `pier serve install`'s dashboard FQDN prompt), `github.com/mattn/go-isatty` (TTY detection for non-interactive bypass). Don't pull a new dep without confirming it's not already in `go.sum`.

## Commit conventions

Follow the existing log style:

- Conventional Commits subject (`feat(scope):`, `fix(scope):`, `revert(scope):`, `refactor(scope):`).
- A reasoning paragraph in the body — "what changed and why this approach", not a diff narration. Tag risks ("file lock prevents X") and explicit non-goals ("doesn't handle Y, Phase 3").
- One commit per logical step. Don't batch a refactor + a feature + a bugfix into one commit. The git log doubles as a tutorial for future contributors.

Branches in flight should not have revert-revert thrashing once a hypothesis is settled. If the branch is unpushed, prefer `git reset --soft <base>` and re-commit as a single coherent change. Once pushed, normal `git revert`.

## Code style

- Don't add comments that restate the code. Write a comment when the **why** is non-obvious: a hidden constraint, a workaround for a specific bug, behaviour that would surprise a reader. Removing the comment shouldn't confuse the reader.
- Don't add error handling, fallbacks, or validation for scenarios that can't happen. Trust internal callers; only validate at boundaries (user input, external APIs).
- Don't backwards-compat for code paths that haven't shipped. Renames are free until the binary is in users' hands.
- `internal/` packages should not import each other unnecessarily. Keep adapter, materialize, infra, headscale, etc. independent so future MCP / dockerfile work can compose them differently.

## Pitfalls (learned the hard way)

These are real bugs that have already cost a session — don't re-learn them.

- **`dig` lies on Linux with tailscale split-DNS.** It hits `/etc/resolv.conf` → 127.0.0.53 stub which doesn't always honour systemd-resolved per-link routing. Test resolution with `resolvectl query` instead. Browsers via `getaddrinfo()` agree with resolvectl.
- **MagicDNS preempts split-DNS for sub-domains of the headscale `base_domain`.** A split-DNS rule for `pier.nebula` (under base `nebula`) never reaches peers as routing — only as a search domain. Workload TLDs must live outside `base_domain`; `extra_records_path` is reserved for the dashboard FQDN.
- **Docker bind-mounts auto-create source dirs as root** when the host path is missing. A subsequent `pier up` then can't write the data. `materialize.ensureSnapshot` rmdirs an empty dst before copying; if rmdir fails (root-owned, user can't), it surfaces a `! skipping ...` warning with a `sudo rm -rf` recovery hint.
- **Distroless / nonroot images break on bind-mounted host paths.** The image's default UID (typically 65532) can't write to host paths owned by UID 1000. The escape hatch is `[stack].match_host_uid = true` in the manifest — pier injects `user: "<uid>:<gid>"` into the compose override.
- **`docker ps --filter ancestor=traefik` doesn't match versioned tags.** Use `docker ps --format '{{.Image}}'` and filter client-side.
- **dnsmasq inside a `--network host`-less container drops UDP replies through docker-proxy** on some Linux kernels. The pier-dnsmasq container must run with `--network host` and `bind-interfaces` (when listen-address is specific).
- **`--listen-address=0.0.0.0` should NOT use `bind-interfaces`** — they conflict. The dnsmasq template emits `bind-interfaces` only for non-wildcard binds.
- **flock on the records JSON file directly is broken** because each writer renames a new file over the inode. Lock a sidecar `.lock` file whose inode is stable; re-read the JSON after acquiring the lock.

## Adding a feature

The pattern that works:

1. Sketch the manifest / config surface change first. Confirm with the user.
2. Implement in `internal/<package>` with unit tests.
3. Wire into `internal/cli/<command>` as a thin pass-through.
4. Smoke test on a real host — the user does this and reports back. Pier touches docker/sudo, so the agent must not try to run them via Bash.
5. One commit per step.

When the feature touches a new infra component (e.g. dockerfile adapter, MCP shim), add a new sub-package under `internal/`. Don't pollute existing packages. The doctor + install + uninstall paths likely need a new branch — keep them obvious with a `cfg.<NewMode> != ""` guard.

Pier is **intentionally docker-coupled**. Even raw-process workloads (uv/npm/cargo) declare a `docker-compose.dev.yml`; this keeps the codebase to a single execution path, avoids host port/PID/log management, and works on any platform docker supports. The process adapter was explicitly dropped — don't reintroduce it without revisiting the decision.

## What NOT to do

- Don't generate documentation files (`.md`) unless the user asks. README.md, AGENTS.md, CLAUDE.md, DESIGN.md are the four — anything else is noise.
- Don't run `sudo` via Bash. The user runs sudo steps themselves.
- Don't push branches without the user asking. Don't merge without a green smoke test.
- Don't change LICENSE without an explicit user decision.
- Don't normalize `main` → `dev` (or any other slug rewriting beyond the conventional-prefix strip + DNS sanitization). The branch name is the slug.

## References

- [DESIGN.md](DESIGN.md) — architecture, modes, open questions.
- [README.md](README.md) — user-facing usage.
- Git log — the canonical history of design choices and failed hypotheses, with reasoning.
