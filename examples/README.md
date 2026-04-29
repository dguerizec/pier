# examples/

Sample projects used to smoke-test pier on real, multi-service stacks.

Each subdirectory is a **standalone git repo** cloned via `sync.sh`. They are
intentionally not submodules: simpler workflow, no `.gitmodules` churn.
Everything inside `examples/` is gitignored except this README and `sync.sh`.

## Setup

```bash
./sync.sh
```

Clones missing repos, fast-forwards existing ones.

## Current

- **web3tiers** — node front + fastapi back + redis. Used to validate compose
  adapter, materialize, and per-worktree isolation.
  Repo: <https://github.com/dguerizec/pier-example-web3tiers>
