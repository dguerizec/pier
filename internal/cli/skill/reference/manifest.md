# pier manifest reference (`.pier.toml`)

`pier init` generates the project / stack / expose blocks and
`[worktree].base_ref` from the compose file, but it does **not**
generate `[env.<service>]` or `[worktree].dir` — those entries are the
main things you'll add by hand (and `[worktree].dir` only on explicit
user request; see [worktree-dir.md](worktree-dir.md)).

## Full annotated example

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
symlinks    = [".env", "secrets/"]   # shared with the primary worktree (read-only intent)
snapshots   = ["data-dev/"]          # copied per worktree (mutable, isolated)
post_create = ["./scripts/seed.sh"]  # shell cmds run after `pier worktree add` materializes
pre_remove  = ["./scripts/dump.sh"]  # shell cmds run before `pier worktree rm` tears down

[worktree]
# dir    = "./worktrees"             # OPTIONAL — only add on explicit user request.
                                     # Per-user default lives in ~/.config/pier/prefs.toml;
                                     # built-in fallback is .pier/worktrees.
                                     # See worktree-dir.md.
base_ref = "main"                    # new branches fork from this ref
```

## Templating tokens

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

## Common `[env.<service>]` patterns

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

## What `pier init` does and doesn't do

- ✅ Asks which detected services to `[[expose]]` and at what port/host.
- ✅ Picks a default service for the bare-slug alias.
- ✅ Writes `[stack]`, `[[expose]]`, `[worktree].base_ref`, sane
  `base_domain`.
- ✅ With `--worktree-dir <path>`: persists that value to
  `~/.config/pier/prefs.toml` (NOT to `.pier.toml`) so it applies
  across every pier project on the host.
- ❌ Does NOT write `[worktree].dir` into `.pier.toml` — that's a
  per-user preference, not a project setting. See
  [worktree-dir.md](worktree-dir.md).
- ❌ Does NOT prompt for `[env.<service>]` — too project-specific.
- ❌ Does NOT add `[materialize]` entries (`symlinks`, `snapshots`,
  `post_create`, `pre_remove`) — add them when the app expects `.env`,
  secrets, a per-worktree mutable data dir, or per-worktree DB
  seed/backup hooks. See [materialize.md](materialize.md).
- ❌ Does NOT add `[hooks]`, `[watch]`, or `match_host_uid` — opt-in.

## Schema-only fields (not yet wired)

The manifest schema accepts a few blocks that pier doesn't act on at
runtime today. Don't recommend them until wiring lands:

- `[hooks].pre_up / post_up / pre_down / post_down` — defined in
  `internal/manifest/manifest.go`, but `pier up` / `pier down` ignore
  them on main. Wiring is in flight on a feature branch. For
  worktree-lifecycle hooks today, use
  `[materialize].post_create` / `pre_remove` (see
  [materialize.md](materialize.md)).
- `[watch].paths / on_change` — `pier watch` returns "not implemented
  yet". Don't suggest a `[watch]` block.
