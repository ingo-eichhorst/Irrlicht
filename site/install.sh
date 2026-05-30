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
UNINSTALL=0
VERSION=""

# Install locations
APP_PATH="/Applications/Irrlicht.app"
DAEMON_PATH="$HOME/.local/bin/irrlichd"
LAUNCHAGENT_PATH="$HOME/Library/LaunchAgents/io.irrlicht.app.daemon.plist"

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
  --uninstall           Remove any existing Irrlicht install and exit
  -h, --help            Show this help

Re-running without --uninstall removes the existing install first,
then installs fresh — no leftover processes or stale files.

What a normal install does:
  • Downloads the signed .zip from the GitHub release
  • Verifies the SHA-256 checksum
  • Strips the quarantine attribute (no Gatekeeper prompts)
  • Installs to /Applications/Irrlicht.app and launches it
EOF
}

# ─── Uninstall previous install ────────────────────────────────────────────
# Removes every variant we may have installed in the past:
# .app bundle, daemon-only binary, LaunchAgent. Leaves user data (logs,
# Application Support) alone.
uninstall_previous() {
    removed_something=0

    # Stop running processes — match any Irrlicht*.app bundle regardless of
    # parent path, so dev builds (/private/tmp/IrrlichtDev.app) and App
    # Translocation ghost paths are cleaned up alongside /Applications/Irrlicht.app.
    if pgrep -f 'Irrlicht[^/]*\.app/Contents/MacOS/Irrlicht' >/dev/null 2>&1; then
        pkill -f 'Irrlicht[^/]*\.app/Contents/MacOS/Irrlicht' 2>/dev/null || true
        removed_something=1
    fi
    if pgrep -x irrlichd >/dev/null 2>&1; then
        pkill -x irrlichd 2>/dev/null || true
        removed_something=1
    fi

    # Unload + remove LaunchAgent (daemon-only installs may have registered one)
    if [ -f "$LAUNCHAGENT_PATH" ]; then
        launchctl unload "$LAUNCHAGENT_PATH" 2>/dev/null || true
        rm -f "$LAUNCHAGENT_PATH"
        removed_something=1
    fi

    # Linux: stop/disable the systemd user unit if a daemon-only install
    # registered one.
    if [ "${PLATFORM:-}" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
        if [ -f "$HOME/.config/systemd/user/irrlichd.service" ]; then
            systemctl --user disable --now irrlichd.service 2>/dev/null || true
            rm -f "$HOME/.config/systemd/user/irrlichd.service"
            systemctl --user daemon-reload 2>/dev/null || true
            removed_something=1
        fi
    fi

    # Remove app bundle
    if [ -d "$APP_PATH" ]; then
        rm -rf "$APP_PATH" 2>/dev/null || fail "Could not remove $APP_PATH (try with sudo?)"
        removed_something=1
    fi

    # Remove daemon-only binary
    if [ -f "$DAEMON_PATH" ]; then
        rm -f "$DAEMON_PATH"
        removed_something=1
    fi

    # Let running processes finish exiting before we install
    [ $removed_something -eq 1 ] && sleep 0.5
    return 0
}

# ─── Parse args ────────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
    case "$1" in
        --daemon-only) DAEMON_ONLY=1; shift ;;
        --uninstall)   UNINSTALL=1; shift ;;
        --version)     VERSION="$2"; shift 2 ;;
        --version=*)   VERSION="${1#*=}"; shift ;;
        -h|--help)     usage; exit 0 ;;
        *) fail "Unknown option: $1 (try --help)" ;;
    esac
done

# ─── Preflight ─────────────────────────────────────────────────────────────

# Platform detection. macOS gets the full menu-bar app; Linux is daemon-only
# (the web dashboard at 127.0.0.1:7837 is its UI — there is no tray app yet).
case "$(uname -s)" in
    Darwin) PLATFORM="darwin" ;;
    Linux)
        PLATFORM="linux"
        if [ "$DAEMON_ONLY" -eq 0 ] && [ "$UNINSTALL" -eq 0 ]; then
            DAEMON_ONLY=1
            warn "Linux has no menu-bar app yet — installing the daemon only."
        fi
        ;;
    *) fail "Unsupported OS: $(uname -s) (Irrlicht supports macOS and Linux)." ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required but not found."

# sha256_verify <dir> <asset> — checksum-check one asset against
# checksums.sha256, using whichever tool the platform ships (shasum on macOS,
# sha256sum on most Linux distros). Both read the "HASH  FILENAME" format.
sha256_verify() {
    _dir="$1"; _asset="$2"
    if command -v shasum >/dev/null 2>&1; then
        (cd "$_dir" && grep " $_asset\$" checksums.sha256 | shasum -a 256 -c --status)
    elif command -v sha256sum >/dev/null 2>&1; then
        (cd "$_dir" && grep " $_asset\$" checksums.sha256 | sha256sum -c --status)
    else
        fail "Need shasum or sha256sum to verify the download."
    fi
}

say ""
say "  ${BOLD}Irrlicht installer${RESET}"
say ""

# ─── Uninstall-only mode ───────────────────────────────────────────────────

if [ "$UNINSTALL" -eq 1 ]; then
    step "Removing existing Irrlicht install"
    uninstall_previous
    ok
    say ""
    say "  ${GREEN}✓${RESET} Irrlicht uninstalled"
    say "  ${DIM}User data in ~/Library/Application Support/Irrlicht/ was kept.${RESET}"
    say ""
    exit 0
fi

# ─── Detect version ────────────────────────────────────────────────────────

if [ -z "$VERSION" ]; then
    step "Detecting latest version"
    # Follow the /releases/latest redirect to avoid GitHub API rate limits
    VERSION=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
        "https://github.com/$REPO/releases/latest" \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+$')
    [ -n "$VERSION" ] || fail "Could not detect latest version"
    printf 'v%s\n' "$VERSION"
fi

# ─── Remove any existing install ───────────────────────────────────────────

step "Removing any existing install"
uninstall_previous
ok

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
    if [ "$PLATFORM" = "darwin" ]; then
        ASSET="irrlichd-darwin-universal.tar.gz"
    else
        case "$(uname -m)" in
            x86_64|amd64)  ARCH="amd64" ;;
            aarch64|arm64) ARCH="arm64" ;;
            *) fail "Unsupported Linux architecture: $(uname -m)" ;;
        esac
        ASSET="irrlichd-linux-${ARCH}.tar.gz"
    fi
    DEST="$HOME/.local/bin/irrlichd"
    UI_DIR="$HOME/.local/share/irrlicht/web"

    step "Downloading $ASSET"
    curl -fsSL -o "$TMPDIR/$ASSET" "$BASE/$ASSET" || fail "Download failed"
    ok

    step "Verifying checksum"
    sha256_verify "$TMPDIR" "$ASSET" || fail "Checksum mismatch — aborting"
    ok

    step "Extracting $ASSET"
    mkdir -p "$TMPDIR/extract"
    tar -xzf "$TMPDIR/$ASSET" -C "$TMPDIR/extract" || fail "Extraction failed"
    ok

    step "Installing to $DEST"
    mkdir -p "$(dirname "$DEST")"
    install -m 755 "$TMPDIR/extract/irrlichd" "$DEST"
    mkdir -p "$UI_DIR"
    install -m 644 "$TMPDIR/extract/web/index.html" "$UI_DIR/index.html"
    install -m 644 "$TMPDIR/extract/web/irrlicht.css" "$UI_DIR/irrlicht.css"
    install -m 644 "$TMPDIR/extract/web/irrlicht.js"  "$UI_DIR/irrlicht.js"
    ok

    # On Linux, install a systemd user unit so the autostart command we print
    # below is actually runnable. We write it but don't enable it — that's the
    # user's call. --uninstall removes it.
    if [ "$PLATFORM" = "linux" ]; then
        step "Writing systemd user unit"
        mkdir -p "$HOME/.config/systemd/user"
        cat > "$HOME/.config/systemd/user/irrlichd.service" <<'UNIT'
[Unit]
Description=Irrlicht — AI coding-agent session monitor
Documentation=https://irrlicht.io/docs/installation.html

[Service]
ExecStart=%h/.local/bin/irrlichd
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
UNIT
        systemctl --user daemon-reload 2>/dev/null || true
        ok
    fi

    say ""
    say "  ${GREEN}✓${RESET} ${BOLD}irrlichd v$VERSION${RESET} installed"
    say ""
    say "  Start the daemon:"
    say "    ${DIM}\$${RESET} $DEST &"
    if [ "$PLATFORM" = "linux" ]; then
        say ""
        say "  Or autostart it with systemd (unit installed; survives logout/reboot):"
        say "    ${DIM}\$${RESET} systemctl --user enable --now irrlichd"
    fi
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
sha256_verify "$TMPDIR" "$ASSET" || fail "Checksum mismatch — aborting"
ok

step "Installing to /Applications"
# ditto preserves macOS metadata including code signatures
ditto -xk "$TMPDIR/$ASSET" /Applications/ || fail "Extract failed"
# Strip any quarantine attribute so Gatekeeper never prompts on first launch.
xattr -dr com.apple.quarantine /Applications/Irrlicht.app 2>/dev/null || true
ok

step "Registering with LaunchServices"
"/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister" \
    -f /Applications/Irrlicht.app 2>/dev/null || true
# Warm Gatekeeper now: the first notarization check on a freshly downloaded
# app can take 10–20s. Doing it here (instead of silently during `open`) means
# the app — and the daemon it spawns — start promptly inside the wait window.
spctl -a -t exec /Applications/Irrlicht.app 2>/dev/null || true
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

printf '%sstill starting%s\n' "$YELLOW" "$RESET"
say ""
warn "Installed. The daemon hadn't responded on port 7837 within 15s —"
warn "macOS may still be verifying the app on this first launch. It should"
warn "come up on its own shortly; the menu bar indicator will appear."
warn "If not, run: open /Applications/Irrlicht.app"
exit 0
