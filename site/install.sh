#!/bin/sh
# Irrlicht installer — https://irrlicht.io
#
# Usage:
#   curl -fsSL https://irrlicht.io/install.sh | sh
#   curl -fsSL https://irrlicht.io/install.sh | sh -s -- --daemon-only
#   curl -fsSL https://irrlicht.io/install.sh | sh -s -- --version 0.3.4
#
# This script installs Irrlicht without going through macOS Gatekeeper
# approval. It downloads the release archive from GitHub, verifies the
# SHA-256 checksum, strips the quarantine attribute, and launches the app.

set -eu

REPO="ingo-eichhorst/Irrlicht"
DAEMON_ONLY=0
VERSION=""

# ─── Helpers ────────────────────────────────────────────────────────────────

if [ -t 1 ]; then
    BOLD=$(printf '\033[1m')
    DIM=$(printf '\033[2m')
    GREEN=$(printf '\033[32m')
    RED=$(printf '\033[31m')
    YELLOW=$(printf '\033[33m')
    RESET=$(printf '\033[0m')
else
    BOLD="" DIM="" GREEN="" RED="" YELLOW="" RESET=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '  %s…%s ' "$*" "$DIM"; }
ok()   { printf '%s✓%s\n' "$GREEN" "$RESET"; }
fail() { printf '%s✗%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }

usage() {
    cat <<'EOF'
Irrlicht installer

Usage:
  curl -fsSL https://irrlicht.io/install.sh | sh
  curl -fsSL https://irrlicht.io/install.sh | sh -s -- [options]

Options:
  --daemon-only         Install only the irrlichd daemon (no menu bar app)
  --version VERSION     Install a specific version (default: latest)
  -h, --help            Show this help

What it does:
  • Downloads the signed .zip from the GitHub release
  • Verifies the SHA-256 checksum
  • Strips the quarantine attribute (no Gatekeeper prompts)
  • Installs to /Applications/Irrlicht.app and launches it
EOF
}

# ─── Parse args ────────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
    case "$1" in
        --daemon-only) DAEMON_ONLY=1; shift ;;
        --version)     VERSION="$2"; shift 2 ;;
        --version=*)   VERSION="${1#*=}"; shift ;;
        -h|--help)     usage; exit 0 ;;
        *) fail "Unknown option: $1 (try --help)" ;;
    esac
done

# ─── Preflight ─────────────────────────────────────────────────────────────

[ "$(uname -s)" = "Darwin" ] || fail "Irrlicht is macOS-only."

command -v curl >/dev/null 2>&1 || fail "curl is required but not found."
command -v shasum >/dev/null 2>&1 || fail "shasum is required but not found."

# ─── Detect version ────────────────────────────────────────────────────────

say ""
say "  ${BOLD}Irrlicht installer${RESET}"
say ""

if [ -z "$VERSION" ]; then
    step "Detecting latest version"
    VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | awk -F'"' '/"tag_name"/ {print $4; exit}' \
        | sed 's/^v//')
    [ -n "$VERSION" ] || fail "Could not detect latest version"
    printf 'v%s\n' "$VERSION"
fi

# ─── Work dir ──────────────────────────────────────────────────────────────

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

BASE="https://github.com/$REPO/releases/download/v${VERSION}"

# ─── Download checksums ────────────────────────────────────────────────────

step "Downloading checksums"
curl -fsSL -o "$TMPDIR/checksums.sha256" "$BASE/checksums.sha256" \
    || fail "Could not download $BASE/checksums.sha256"
ok

# ─── Daemon-only path ──────────────────────────────────────────────────────

if [ "$DAEMON_ONLY" -eq 1 ]; then
    ASSET="irrlichd-darwin-universal"
    DEST="$HOME/.local/bin/irrlichd"

    step "Downloading $ASSET"
    curl -fsSL -o "$TMPDIR/$ASSET" "$BASE/$ASSET" || fail "Download failed"
    ok

    step "Verifying checksum"
    (cd "$TMPDIR" && grep " $ASSET\$" checksums.sha256 | shasum -a 256 -c --status) \
        || fail "Checksum mismatch — aborting"
    ok

    step "Installing to $DEST"
    mkdir -p "$(dirname "$DEST")"
    install -m 755 "$TMPDIR/$ASSET" "$DEST"
    ok

    say ""
    say "  ${GREEN}✓${RESET} ${BOLD}irrlichd v$VERSION${RESET} installed"
    say ""
    say "  Start the daemon:"
    say "    ${DIM}\$${RESET} $DEST &"
    say ""
    say "  Dashboard will be at ${BOLD}http://127.0.0.1:7837${RESET}"
    say ""
    exit 0
fi

# ─── Full install path ─────────────────────────────────────────────────────

ASSET="Irrlicht-${VERSION}.zip"

step "Downloading $ASSET"
curl -fsSL -o "$TMPDIR/$ASSET" "$BASE/$ASSET" \
    || fail "Download failed — does this version have a .zip asset? (see --help)"
ok

step "Verifying checksum"
(cd "$TMPDIR" && grep " $ASSET\$" checksums.sha256 | shasum -a 256 -c --status) \
    || fail "Checksum mismatch — aborting"
ok

# Stop running instances so we can replace them
step "Stopping running instances"
pkill -f '/Applications/Irrlicht.app/Contents/MacOS/Irrlicht' 2>/dev/null || true
pkill -x irrlichd 2>/dev/null || true
# App Translocation paths
pkill -f 'AppTranslocation.*Irrlicht' 2>/dev/null || true
sleep 0.5
ok

step "Installing to /Applications"
# ditto preserves macOS metadata including code signatures
if [ -d /Applications/Irrlicht.app ]; then
    rm -rf /Applications/Irrlicht.app 2>/dev/null \
        || fail "Could not remove old /Applications/Irrlicht.app (try with sudo?)"
fi
ditto -xk "$TMPDIR/$ASSET" /Applications/ || fail "Extract failed"
ok

step "Stripping quarantine attribute"
xattr -cr /Applications/Irrlicht.app 2>/dev/null || true
ok

step "Launching Irrlicht"
open /Applications/Irrlicht.app
ok

# ─── Verify ────────────────────────────────────────────────────────────────

step "Waiting for daemon to start"
i=0
while [ $i -lt 15 ]; do
    if curl -sf -m 1 http://127.0.0.1:7837/state >/dev/null 2>&1; then
        ok
        say ""
        say "  ${GREEN}✓${RESET} ${BOLD}Irrlicht v$VERSION${RESET} installed and running"
        say ""
        say "  Dashboard: ${BOLD}http://127.0.0.1:7837${RESET}"
        say "  Menu bar:  look for the Irrlicht indicator"
        say ""
        exit 0
    fi
    sleep 1
    i=$((i + 1))
done

printf '%stimeout%s\n' "$YELLOW" "$RESET"
say ""
warn "Installed, but the daemon didn't respond on port 7837 within 15s."
warn "Try: open /Applications/Irrlicht.app"
exit 1
