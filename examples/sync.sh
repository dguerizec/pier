#!/usr/bin/env bash
# Clone or fast-forward the example projects used to exercise pier.
# These repos live under examples/ but are independent — never committed
# into pier itself (see examples/.gitignore).
set -euo pipefail

cd "$(dirname "$0")"

REPOS=(
  "web3tiers|git@github.com:dguerizec/pier-example-web3tiers.git"
)

for entry in "${REPOS[@]}"; do
  name="${entry%%|*}"
  url="${entry#*|}"
  if [[ -d "$name/.git" ]]; then
    echo "==> $name: pulling"
    git -C "$name" pull --ff-only
  else
    echo "==> $name: cloning from $url"
    git clone "$url" "$name"
  fi
done
