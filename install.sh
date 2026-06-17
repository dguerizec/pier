#!/bin/sh
# pier installer — POSIX sh, no bashisms.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dguerizec/pier/main/install.sh | sh
#
# Environment:
#   PIER_VERSION   tag to install (default: latest release). Examples: v0.1.0, v0.0.1-rc1
#   PIER_INSTALL_DIR  override install directory (default: ~/.local/bin if writable, else /usr/local/bin via sudo)
#   PIER_REPO      override repo slug (default: dguerizec/pier)

set -eu

REPO="${PIER_REPO:-dguerizec/pier}"
VERSION="${PIER_VERSION:-}"

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"; }

download_json() {
    out="$1"
    url="$2"
    attempts=3
    attempt=1
    while [ "$attempt" -le "$attempts" ]; do
        if $DL_OUT "$out" "$url"; then
            return 0
        fi
        if [ "$attempt" -lt "$attempts" ]; then
            warn "download failed, retrying ($attempt/$attempts): $url"
            sleep 2
        fi
        attempt=$((attempt + 1))
    done
    return 1
}

# tar is non-negotiable.
need tar

# Pick a downloader.
if command -v curl >/dev/null 2>&1; then
    DL="curl -fsSL"
    DL_OUT="curl -fsSL -o"
elif command -v wget >/dev/null 2>&1; then
    DL="wget -qO-"
    DL_OUT="wget -qO"
else
    die "need curl or wget"
fi

# Pick a sha256 verifier.
if command -v sha256sum >/dev/null 2>&1; then
    SHA="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    SHA="shasum -a 256"
else
    die "need sha256sum or shasum"
fi

# Detect OS — archive name template uses title-case (Linux, Darwin).
uname_s="$(uname -s)"
case "$uname_s" in
    Linux)  OS="Linux"  ;;
    Darwin) OS="Darwin" ;;
    *) die "unsupported OS: $uname_s (pier supports Linux and macOS)" ;;
esac

# Detect arch — archive name uses x86_64 and arm64.
uname_m="$(uname -m)"
case "$uname_m" in
    x86_64|amd64)  ARCH="x86_64" ;;
    arm64|aarch64) ARCH="arm64"  ;;
    *) die "unsupported arch: $uname_m (pier supports x86_64 and arm64)" ;;
esac

# Stage in a temp dir; clean up on exit (success or failure).
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

# Resolve version.
if [ -z "$VERSION" ]; then
    log "resolving latest release for $REPO"
    VERSION_JSON="$TMP/release.json"
    if download_json "$VERSION_JSON" "https://api.github.com/repos/${REPO}/releases/latest"; then
        VERSION="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$VERSION_JSON" | head -n1)"
    fi
    if [ -z "$VERSION" ]; then
        warn "latest release endpoint failed; falling back to release list"
        download_json "$VERSION_JSON" "https://api.github.com/repos/${REPO}/releases?per_page=1" \
            || die "could not resolve latest release tag"
        VERSION="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$VERSION_JSON" | head -n1)"
    fi
    [ -n "$VERSION" ] || die "could not resolve latest release tag"
fi

VERSION_NUM="${VERSION#v}"
ARCHIVE="pier_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"

log "installing pier $VERSION ($OS/$ARCH)"

cd "$TMP"

log "downloading $ARCHIVE"
$DL_OUT "$ARCHIVE" "${BASE}/${ARCHIVE}" \
    || die "download failed: ${BASE}/${ARCHIVE}"

log "downloading checksums.txt"
$DL_OUT "checksums.txt" "${BASE}/checksums.txt" \
    || die "checksums.txt download failed"

log "verifying sha256"
# Filter checksums.txt to just the file we downloaded so other archives'
# missing files don't make the verifier fail.
grep " ${ARCHIVE}\$" checksums.txt > checksums.filtered \
    || die "no checksum line for $ARCHIVE in checksums.txt"
$SHA -c checksums.filtered >/dev/null \
    || die "sha256 mismatch for $ARCHIVE"

log "extracting"
tar xzf "$ARCHIVE"
[ -f pier ] || die "archive did not contain a pier binary"
chmod +x pier

# Resolve install directory.
if [ -n "${PIER_INSTALL_DIR:-}" ]; then
    DEST="$PIER_INSTALL_DIR"
    SUDO=""
elif [ -d "$HOME/.local/bin" ] && [ -w "$HOME/.local/bin" ]; then
    DEST="$HOME/.local/bin"
    SUDO=""
elif mkdir -p "$HOME/.local/bin" 2>/dev/null && [ -w "$HOME/.local/bin" ]; then
    DEST="$HOME/.local/bin"
    SUDO=""
else
    DEST="/usr/local/bin"
    if [ -w "$DEST" ]; then
        SUDO=""
    else
        command -v sudo >/dev/null 2>&1 || die "$DEST not writable and sudo unavailable"
        SUDO="sudo"
    fi
fi

log "installing to $DEST/pier${SUDO:+ (using sudo)}"
$SUDO install -m 0755 pier "$DEST/pier"

log "done. pier installed at $DEST/pier"
"$DEST/pier" --version || true

case ":${PATH}:" in
    *":${DEST}:"*) ;;
    *) warn "$DEST is not in your PATH; add it to your shell profile" ;;
esac
