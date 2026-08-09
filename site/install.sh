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

# Install locations.
#
# APP_PATH is the only one that is not under $HOME, which makes it the only one
# a test cannot isolate by pointing HOME at a temp dir — and uninstall_previous
# `rm -rf`s it. The override exists so tools/lib/install-uninstall_test.sh can
# exercise the real uninstall path without deleting a developer's actual
# /Applications/Irrlicht.app (and without silently passing on CI, where that
# path happens not to exist).
APP_PATH="${IRRLICHT_TEST_APP_PATH:-/Applications/Irrlicht.app}"
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

# fetch downloads over HTTPS, following redirects with the protocol pinned.
# -L is required — GitHub release assets redirect to a CDN — but on its own it
# lets a redirect target choose the protocol, so a tampered or hijacked
# Location header could downgrade a release download to plaintext http and
# hand us a substituted binary. --proto pins the first request and
# --proto-redir every hop after it.
fetch() { curl -fsSL --proto '=https' --proto-redir '=https' "$@"; }

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

# ─── Agent hook cleanup ────────────────────────────────────────────────────
# Ask irrlichd to remove every hook entry it wrote into the user's agent config
# files (~/.claude/settings.json, ~/.codex/hooks.json, …). Without this an
# uninstalled irrlicht stays in the user's agent configuration, pointing at a
# binary that no longer exists (#1416).
#
# The adapter set is resolved from the adapter registry inside the binary
# (#1357), so there is deliberately NO list of agents or config paths here: a
# second list is a list that drifts as adapters are added, which is the bug
# #1357 fixed on the Go side.
#
# Only the real `--uninstall` calls this — see the call in uninstall_previous.
uninstall_agent_hooks() {
    # Both install flavours, in the order the installer creates them: the
    # daemon-only binary, then the daemon inside the .app bundle. Deliberately
    # NOT `command -v irrlichd`: that can resolve to an unrelated dev build in
    # someone's worktree, and this is the one place we are about to rewrite the
    # user's agent configs from whatever we find.
    _irrlichd=""
    for _candidate in "$DAEMON_PATH" "$APP_PATH/Contents/MacOS/irrlichd"; do
        if [ -x "$_candidate" ]; then
            _irrlichd="$_candidate"
            break
        fi
    done

    # A partial earlier uninstall, or a hand-deleted binary. There is nothing
    # left to run the sweep with; say so and carry on. Failing the uninstall
    # over this would strand the user with a half-removed product, which is
    # strictly worse than the residue we are trying to clean up.
    if [ -z "$_irrlichd" ]; then
        # Close the caller's pending `step` line so the warning starts on its own.
        printf '\n'
        warn "No irrlichd binary found — skipped the agent hook cleanup. If you had"
        warn "  hooks installed, clear them from ~/.claude/settings.json and ~/.codex/hooks.json."
        return 0
    fi

    # Run the sweep detached from our stdin and under a bounded wait.
    #
    # irrlichd has no argument parser: an unknown flag falls through to STARTING
    # A DAEMON (#1417 — core/cmd/irrlichd/dispatch_test.go pins that as
    # deliberate). --uninstall-hooks has existed since v0.3.4, so a current
    # binary is fine, but someone uninstalling an older one would otherwise boot
    # a daemon right here — binding the port this function just freed — and
    # block the script forever reading its output. An uninstall that hangs is
    # the worst outcome available: `curl | sh` gives the child our stdin too,
    # and a Ctrl-C leaves a half-removed product plus a live daemon.
    #
    # A bounded wait is the general form of the guard. A version check would
    # cover only the versions we know about today, and would have to run the
    # same binary to ask.
    # 30 ticks x 0.5s = 15s. Overridable only so the test can exercise the
    # timeout branch in a second instead of fifteen — same test seam as
    # IRRLICHT_TEST_APP_PATH above, and like it, guarded by an assertion in
    # tools/lib/install-uninstall_test.sh that the override still exists.
    _hook_ticks="${IRRLICHT_HOOK_SWEEP_TICKS:-30}"
    if ! _hook_log=$(mktemp); then
        printf '\n'
        warn "Could not create a temp file for the hook cleanup — skipping it."
        return 0
    fi
    "$_irrlichd" --uninstall-hooks >"$_hook_log" 2>&1 </dev/null &
    _hook_pid=$!
    _hook_waited=0
    while kill -0 "$_hook_pid" 2>/dev/null; do
        if [ "$_hook_waited" -ge "$_hook_ticks" ]; then
            kill "$_hook_pid" 2>/dev/null || true
            printf '\n'
            warn "irrlichd --uninstall-hooks did not finish in time — stopped it."
            warn "  This binary may predate the flag (it needs v0.3.4 or newer)."
            warn "  Check ~/.claude/settings.json and ~/.codex/hooks.json for stale entries."
            rm -f "$_hook_log"
            return 0
        fi
        sleep 0.5
        _hook_waited=$((_hook_waited + 1))
    done

    # Fail soft, always: the user asked for removal. `set -e` would abort the
    # whole uninstall on a non-zero exit, so the wait is a condition rather than
    # a bare statement. The binary's own output is replayed only on failure, so
    # a healthy sweep stays quiet inside the caller's one-line progress step.
    if wait "$_hook_pid"; then
        rm -f "$_hook_log"
        return 0
    fi
    printf '\n'
    warn "Could not remove irrlicht's hook entries from the agent configs:"
    [ -s "$_hook_log" ] && cat "$_hook_log" >&2
    warn "  Check ~/.claude/settings.json and ~/.codex/hooks.json for stale entries."
    rm -f "$_hook_log"
    return 0
}

# ─── Uninstall previous install ────────────────────────────────────────────
# Removes every variant we may have installed in the past:
# .app bundle, daemon-only binary, LaunchAgent. Leaves user data (logs,
# Application Support) alone.
#
# $1 — pass 1 to also sweep irrlicht's hook entries out of the user's agent
# configs. Opt-in, and only the real `--uninstall` opts in. This function also
# runs on the re-install/upgrade path, and `--uninstall-hooks` does more than
# edit config files: it records the hooks permissions as DENIED (#570), so that
# a persisted "granted" cannot re-install them on the next daemon start. That
# denial is written into the user data an uninstall deliberately KEEPS — so
# sweeping on an upgrade would silently switch hook-based monitoring off for a
# user who only asked for a newer version.
uninstall_previous() {
    sweep_agent_hooks="${1:-0}"
    removed_something=0

    # Stop running processes — match any Irrlicht*.app bundle regardless of
    # parent path, so dev builds (/private/tmp/IrrlichtDev.app) and App
    # Translocation ghost paths are cleaned up alongside /Applications/Irrlicht.app.
    if pgrep -f 'Irrlicht[^/]*\.app/Contents/MacOS/Irrlicht' >/dev/null 2>&1; then
        pkill -f 'Irrlicht[^/]*\.app/Contents/MacOS/Irrlicht' 2>/dev/null || true
        removed_something=1
    fi
    # Stop any irrlichd bound to our port. Matching by process name alone
    # (like the .app pkill above) would silently kill an unrelated irrlichd
    # — a dev `--record` daemon on a different port, or another user's
    # process. Scope the kill to whatever is actually listening on 7837;
    # when we can't confirm that (no lsof), warn instead of guessing.
    if command -v lsof >/dev/null 2>&1; then
        daemon_pids=$(lsof -ti tcp:7837 -sTCP:LISTEN 2>/dev/null || true)
        for pid in $daemon_pids; do
            case "$(basename "$(ps -o comm= -p "$pid" 2>/dev/null)" 2>/dev/null)" in
                irrlichd)
                    kill "$pid" 2>/dev/null || true
                    removed_something=1
                    ;;
                *)
                    ;;
            esac
        done
    elif pgrep -x irrlichd >/dev/null 2>&1; then
        for pid in $(pgrep -x irrlichd); do
            cmd=$(ps -o comm= -p "$pid" 2>/dev/null)
            warn "Stopping irrlichd (pid $pid, $cmd) — no lsof available to confirm it's bound to port 7837"
            kill "$pid" 2>/dev/null || true
        done
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
    if [ "${PLATFORM:-}" = "linux" ] && command -v systemctl >/dev/null 2>&1 && [ -f "$HOME/.config/systemd/user/irrlichd.service" ]; then
        systemctl --user disable --now irrlichd.service 2>/dev/null || true
        rm -f "$HOME/.config/systemd/user/irrlichd.service"
        systemctl --user daemon-reload 2>/dev/null || true
        removed_something=1
    fi

    # Clean irrlicht out of the agent configs while a binary that can do it
    # still exists — both places we might find one are removed just below.
    # Placed after the daemon is stopped and its LaunchAgent/systemd unit are
    # gone, so nothing can restart and re-apply the entries we just removed.
    if [ "$sweep_agent_hooks" -eq 1 ]; then
        uninstall_agent_hooks
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
    uninstall_previous 1
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
    VERSION=$(fetch -o /dev/null -w '%{url_effective}' \
        "https://github.com/$REPO/releases/latest" \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+$')
    [ -n "$VERSION" ] || fail "Could not detect latest version"
    printf 'v%s\n' "$VERSION"
fi

# ─── Work dir ──────────────────────────────────────────────────────────────
#
# Note: the existing install is NOT removed here. We download and verify the
# new artifact into TMPDIR first, and only remove the old install immediately
# before extracting the verified bytes (full-install path below). This keeps
# the working install intact when a download fails — a transient GitHub CDN
# 504 used to leave the user with nothing because removal happened up front.
# The --daemon-only path never removes /Applications/Irrlicht.app at all (it
# installs irrlichd under ~/.local and overwrites in place).

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

BASE="https://github.com/$REPO/releases/download/v${VERSION}"

# ─── Download checksums ────────────────────────────────────────────────────

step "Downloading checksums"
fetch -o "$TMPDIR/checksums.sha256" "$BASE/checksums.sha256" \
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
    fetch -o "$TMPDIR/$ASSET" "$BASE/$ASSET" || fail "Download failed"
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
fetch -o "$TMPDIR/$ASSET" "$BASE/$ASSET" \
    || fail "Download failed — does this version have a .zip asset? (see --help)"
ok

step "Verifying checksum"
sha256_verify "$TMPDIR" "$ASSET" || fail "Checksum mismatch — aborting"
ok

# Only now that we hold verified bytes do we remove the previous install —
# a failed download above can never leave the user without a working app.
step "Removing any existing install"
uninstall_previous
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

# Put the irrlicht-ls CLI on PATH (#608). Runs as the invoking user — no
# sudo escalation: try /usr/local/bin, fall back to Homebrew's bin (user-
# writable on Apple Silicon), otherwise print the one-liner. The app's
# Settings panel offers the same install for drag-install users.
step "Linking irrlicht-ls CLI"
if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    ln -sf /Applications/Irrlicht.app/Contents/MacOS/irrlicht-ls /usr/local/bin/irrlicht-ls
    ok
elif [ -d /opt/homebrew/bin ] && [ -w /opt/homebrew/bin ]; then
    ln -sf /Applications/Irrlicht.app/Contents/MacOS/irrlicht-ls /opt/homebrew/bin/irrlicht-ls
    ok
else
    printf '%sskipped%s\n' "$YELLOW" "$RESET"
    warn "No user-writable bin dir found for the irrlicht-ls CLI. Add it with:"
    warn "  sudo ln -sf /Applications/Irrlicht.app/Contents/MacOS/irrlicht-ls /usr/local/bin/irrlicht-ls"
fi

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
