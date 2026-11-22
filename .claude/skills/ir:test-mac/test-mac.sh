#!/usr/bin/env bash
# test-mac.sh — build and run the irrlicht dev stack (Go daemon and/or macOS
# Swift app) for local testing. This is the executable body of the
# `ir:test-mac` skill; SKILL.md next to this file says WHEN to reach for each
# axis and what each one costs, and this script is the only place the
# procedure itself lives.
#
# Usage:
#   .claude/skills/ir:test-mac/test-mac.sh [MODE] [TARGET]
#   .claude/skills/ir:test-mac/test-mac.sh --help
#
# Both arguments are optional, may be given in either order, and default to
# `replace full`.
#
#   MODE    replace  (default) — take over from production: port 7837, the
#                     production state dir (no IRRLICHT_HOME override), and the
#                     Swift app installed straight into /Applications/Irrlicht.app.
#                     DESTRUCTIVE — run restore-prod.sh (same directory) to get
#                     production back; quitting the app is NOT enough.
#           separate — coexist with production: port 7838, an isolated
#                     IRRLICHT_HOME under the repo's .build/, and the app
#                     assembled at /tmp/IrrlichtDev.app.
#
#   TARGET  full     (default) — rebuild and restart both daemon and app.
#           daemon   — rebuild + restart irrlichd only; leave whatever app is
#                     running to reconnect.
#           macos    — rebuild + relaunch the app only, against whatever daemon
#                     is already up. It does NOT start one.
#
#   SIGNING the app is signed with, in order of preference: the identity named
#           by IRRLICHT_DEV_SIGN_IDENTITY ("-" = ad-hoc), else the first
#           "Developer ID Application:" identity in the keychain, else the
#           self-signed "Irrlicht Dev" (tools/dev-sign-setup.sh), else ad-hoc.
#           Only a Developer ID signature satisfies the relay-token keychain
#           item's requirement, so only it reads the token without a prompt.
#
# WHY THIS IS A SCRIPT (#1855). The procedure used to live as nine fenced bash
# blocks in SKILL.md that an agent copied out and ran one at a time. Every step
# branches on MODE/TARGET/PORT/DEV_HOME/SOCK, and each fenced block runs in a
# FRESH shell — so a value dropped between blocks failed SILENTLY: an empty
# $PORT made the daemon bind `127.0.0.1:` (invalid) and an empty $SOCK turned
# the socket cleanup into a no-op that reported nothing. One script is one
# variable scope, and that whole class is gone.
#
# The gates below are load-bearing; each one names the failure it prevents.
# tools/lib/test-mac-script_test.sh drives this script against stubs and
# asserts every one of them, and tools/lib/test-mac-script-mutations_test.sh
# breaks each gate in turn and requires that lock test to go red.
set -euo pipefail

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  # Matches tools/bash-lint.sh, tools/mutate.sh, tools/posix-lint.sh,
  # tools/preflight.sh, tools/security-scan.sh and tools/skill-lint.sh
  # BYTE-FOR-BYTE, so --help can't drift into a seventh self-help format.
  awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"
  exit 0
fi

# ─── Step 0a: the two axes, validated against their literal enums ───────────
# There is NO default branch here on purpose. Before this was a script, every
# later gate was an exact-string comparison against $MODE/$TARGET, so a typo
# ("seperate") matched nothing and silently no-opped every step instead of
# erroring. An unrecognised argument is a hard refusal.
MODE="replace"
TARGET="full"
mode_given=""
target_given=""
for arg in "$@"; do
  case "$arg" in
    replace|separate)
      if [[ -n "$mode_given" ]]; then
        echo "ERROR: MODE given twice ('$mode_given' then '$arg')." >&2
        exit 2
      fi
      MODE="$arg"; mode_given="$arg" ;;
    full|daemon|macos)
      if [[ -n "$target_given" ]]; then
        echo "ERROR: TARGET given twice ('$target_given' then '$arg')." >&2
        exit 2
      fi
      TARGET="$arg"; target_given="$arg" ;;
    *)
      echo "ERROR: unrecognised argument '$arg'." >&2
      echo "       MODE must be 'replace' or 'separate'; TARGET must be 'full', 'daemon' or 'macos'." >&2
      echo "       usage: test-mac.sh [MODE] [TARGET]   (see --help)" >&2
      exit 2 ;;
  esac
done

# ─── Step 0b: paths ────────────────────────────────────────────────────────
# Each has an env override so tools/lib/test-mac-script_test.sh can point the
# whole procedure at a temp dir and exercise the gates without touching the
# real machine. They are a TEST SEAM, not a supported way to run this by hand:
# every real run uses the defaults.
REPO_ROOT="${IRRLICHT_TESTMAC_REPO_ROOT:-$(git rev-parse --show-toplevel)}"
# $MAIN_REPO is deliberately the main checkout and not $REPO_ROOT: the daemon
# binary path must be stable so the build and launch steps agree even when
# $REPO_ROOT is a worktree (the SOURCE compiled is still the worktree's — the
# `go build` below runs in $REPO_ROOT/core), and the production backup must
# survive a worktree being removed.
MAIN_REPO="${IRRLICHT_TESTMAC_MAIN_REPO:-/Users/ingo/projects/irrlicht}"
PROD_APP="${IRRLICHT_TESTMAC_PROD_APP:-/Applications/Irrlicht.app}"
DEV_APP="${IRRLICHT_TESTMAC_DEV_APP:-/tmp/IrrlichtDev.app}"
LOG_DIR="${IRRLICHT_TESTMAC_LOG_DIR:-/tmp}"
PLISTBUDDY="${IRRLICHT_TESTMAC_PLISTBUDDY:-/usr/libexec/PlistBuddy}"

IRRLICHTD_BIN="$MAIN_REPO/core/bin/irrlichd"
PROD_BACKUP="$MAIN_REPO/.build/irrlicht-prod-backup/Irrlicht.app"
SWIFT_SRC_DIR="$REPO_ROOT/platforms/macos/Irrlicht"
DEBUG_DIR="$REPO_ROOT/platforms/macos/.build/arm64-apple-macosx/debug"
DEBUG_BIN="$DEBUG_DIR/Irrlicht"
DEBUG_SPARKLE="$DEBUG_DIR/Sparkle.framework"
ENTITLEMENTS="$SWIFT_SRC_DIR/Resources/Irrlicht-dev.entitlements"
NL=$'\n'   # line-start anchor for the in-shell codesign match below
# The CFBundleShortVersionString every dev install stamps into the bundle. It
# is the ONLY thing that tells a dev build from an untouched release once both
# carry a Developer ID signature (step 5). restore-prod.sh reads the same
# literal; tools/lib/test-mac-script_test.sh's restore-after-dev-run case runs
# this script and then that one to prove the two still agree.
DEV_VERSION_MARKER="dev"

# ─── Step 0c: everything the two axes derive ───────────────────────────────
if [[ "$MODE" == "replace" ]]; then
  PORT=7837                                          # production port
  DEV_HOME=""                                        # no IRRLICHT_HOME override → production state dir
  SOCK="$HOME/.local/share/irrlicht/irrlichd.sock"   # production socket
  APP_TARGET="$PROD_APP"
  APP_KILL_PATTERN="Irrlicht\.app/Contents/MacOS/Irrlicht"
else
  PORT=7838                                          # isolated dev port
  DEV_HOME="$REPO_ROOT/.build/irrlicht-home"         # isolated state dir (IRRLICHT_HOME)
  SOCK="$DEV_HOME/irrlichd.sock"
  APP_TARGET="$DEV_APP"
  APP_KILL_PATTERN="IrrlichtDev"
  mkdir -p "$DEV_HOME"
fi

want_daemon() { [[ "$TARGET" == "daemon" || "$TARGET" == "full" ]]; }
want_app()    { [[ "$TARGET" == "macos"  || "$TARGET" == "full" ]]; }

# ─── Abort safety net ──────────────────────────────────────────────────────
# Once the replace-mode install has written into $PROD_APP, ANY later failure
# leaves the PRODUCTION bundle holding a dev build. Several such paths exist and
# are not hypothetical: step 7's reachability ABORT fires after the install by
# design, and under `set -e` so do install_name_tool (it exits 1 on a duplicate
# LC_RPATH), PlistBuddy on a missing key, codesign, and open. Without this the
# user is left with a silently dev-ified /Applications/Irrlicht.app and no hint
# that a teardown exists (#1855 review F5).
BUNDLE_DIRTIED=""
on_exit() {
  local rc=$?
  if [[ $rc -ne 0 && -n "$BUNDLE_DIRTIED" ]]; then
    echo >&2
    echo "NOTE: $PROD_APP now holds a DEV build — this run overwrote it and then failed." >&2
    echo "      Restore production with:  $(dirname "$0")/restore-prod.sh" >&2
  fi
  return "$rc"
}
trap on_exit EXIT

echo "MODE=$MODE TARGET=$TARGET PORT=$PORT DEV_HOME=${DEV_HOME:-<production>}"

# ─── Step 0d: the signing identity, resolved BEFORE anything is touched ─────
# Preference: IRRLICHT_DEV_SIGN_IDENTITY ("-" = ad-hoc) > the first
# "Developer ID Application:" identity > the self-signed "Irrlicht Dev" >
# ad-hoc. Developer ID comes first because the relay token's keychain item
# (service ai.irrlicht.relay) trusts the release app by REQUIREMENT —
# identifier io.irrlicht.app, an Apple Developer ID chain, and the team OU —
# and only a Developer ID signature satisfies it. Any other signature makes
# every read of the token raise a keychain prompt. It also gives the dev build
# the release's designated requirement, which is what TCC grants key on
# (measured: `codesign -dr -` on a scratch bundle signed this way printed the
# installed release's designated requirement verbatim).
#
# Resolved here rather than at the signing step so a bad override is refused
# before the running app is killed or the production bundle is overwritten,
# not discovered by codesign afterwards.
#
# Matched in-shell, not piped, for the same reason as the codesign check in
# step 5: a keychain with many identities would eventually outgrow the pipe
# buffer, and the failure mode is silent — the match fails and the build
# drops to a weaker signature. Parameter expansion runs no second process.
if want_app; then
  IDENTITIES="$(security find-identity -v -p codesigning 2>/dev/null || true)"
  SIGN_FALLBACK_NOTE=""
  if [[ -n "${IRRLICHT_DEV_SIGN_IDENTITY:-}" ]]; then
    SIGN_IDENTITY="$IRRLICHT_DEV_SIGN_IDENTITY"
    if [[ "$SIGN_IDENTITY" != "-" && "$IDENTITIES" != *"$SIGN_IDENTITY"* ]]; then
      echo "ERROR: IRRLICHT_DEV_SIGN_IDENTITY='$SIGN_IDENTITY' matches no valid codesigning identity" >&2
      echo "       (security find-identity -v -p codesigning). Unset it, or use '-' for ad-hoc." >&2
      exit 1
    fi
  elif [[ "$IDENTITIES" == *'"Developer ID Application: '* ]]; then
    SIGN_IDENTITY="${IDENTITIES#*\"Developer ID Application: }"   # → "<name> (<team>)\" …rest"
    SIGN_IDENTITY="Developer ID Application: ${SIGN_IDENTITY%%\"*}"
  elif [[ "$IDENTITIES" == *'"Irrlicht Dev"'* ]]; then
    SIGN_IDENTITY="Irrlicht Dev"
    SIGN_FALLBACK_NOTE="with the self-signed \"Irrlicht Dev\""
  else
    SIGN_IDENTITY="-"
    SIGN_FALLBACK_NOTE="ad-hoc (TCC grants reset on every rebuild; run tools/dev-sign-setup.sh for a stable identity)"
  fi
  if [[ "$SIGN_IDENTITY" == "-" ]]; then SIGN_LABEL="ad-hoc (-)"; else SIGN_LABEL="$SIGN_IDENTITY"; fi
  echo "Signing identity: $SIGN_LABEL"
  if [[ -n "$SIGN_FALLBACK_NOTE" ]]; then
    echo "NOTE: no Developer ID identity found — signing $SIGN_FALLBACK_NOTE. This build fails the relay-token"
    echo "      keychain item's requirement, so every token read will raise a keychain prompt."
  fi
fi

# ─── Step 1: build the Go daemon ───────────────────────────────────────────
# The daemon resolves the dashboard from platforms/web/index.html at runtime
# via a walk-up search from its own executable; no embed, no codegen.
if want_daemon; then
  echo "Building irrlichd → $IRRLICHTD_BIN …"
  ( cd "$REPO_ROOT/core" && go build -o "$IRRLICHTD_BIN" ./cmd/irrlichd )
fi

# ─── Step 2: build the Swift app (compile only; no bundle mutation yet) ────
# Safe before killing anything: it only writes into .build/. The install into
# a live bundle happens after the kill step below.
if want_app; then
  echo "Building the Swift app …"
  # NOT a bare `swift build … | tail -5`: `set -o pipefail` makes the pipeline
  # report swift's status rather than tail's, and the explicit branch says so
  # out loud instead of dying with no message.
  if ! swift build --package-path "$REPO_ROOT/platforms/macos" 2>&1 | tail -5; then
    echo "ERROR: swift build failed — not touching $APP_TARGET." >&2
    exit 1
  fi
fi

# ─── Step 3: kill the instances this mode+target replaces ──────────────────
# The APP IS KILLED BEFORE THE DAEMON (matching restore-prod.sh's order) so a
# still-alive app never observes a momentary daemon-less gap and reacts by
# spawning its own replacement, which could win the port race against the
# daemon step 6 starts next. Daemon and app are killed independently so a
# TARGET=daemon/macos run leaves the other component alone.
if want_app; then
  echo "Stopping the app ($APP_KILL_PATTERN) …"
  pkill -f "$APP_KILL_PATTERN" 2>/dev/null || true
  if [[ "$MODE" == "replace" ]]; then
    pkill -f "IrrlichtDev" 2>/dev/null || true   # any leftover separate-mode dev app, for tidiness
  fi
  # Step 5 is about to back up / overwrite this same app's on-disk bundle in
  # replace mode. Wait for the process to ACTUALLY exit rather than sleeping a
  # flat interval, so we never copy or overwrite files a dying process still
  # has open — and hard-abort if it outlives the wait.
  for _ in 1 2 3 4 5; do
    pgrep -f "$APP_KILL_PATTERN" >/dev/null 2>&1 || break
    sleep 1
  done
  if [[ "$MODE" == "replace" ]] && pgrep -f "$APP_KILL_PATTERN" >/dev/null 2>&1; then
    echo "ABORT: the app process is still running — refusing to overwrite its bundle in step 5." >&2
    exit 1
  fi
fi

if want_daemon; then
  echo "Stopping the daemon on $PORT …"
  if [[ "$MODE" == "replace" ]]; then
    pkill -x "irrlichd" 2>/dev/null || true          # ALL daemons by exact name (prod's bundled one + any standalone dev one)
  else
    pkill -f "core/bin/irrlichd" 2>/dev/null || true # prior dev daemon only (NOT production)
  fi
  PORT_PIDS="$(lsof -ti tcp:"$PORT" 2>/dev/null || true)"   # belt-and-suspenders: anything still on the target port
  if [[ -n "$PORT_PIDS" ]]; then
    # shellcheck disable=SC2086  # deliberately unquoted: lsof -ti prints one PID per line and kill takes them as separate arguments
    kill $PORT_PIDS 2>/dev/null || true
  fi
  sleep 1

  # ─── Step 4: clean up the stale socket for the target instance ───────────
  rm -f "$SOCK"
fi

# ─── Step 5: install the app bundle ────────────────────────────────────────
if want_app; then
  # Build-output gates, hoisted ahead of BOTH modes' install so a failed or
  # stale `swift build` aborts here instead of leaving Sparkle.framework
  # deleted with nothing to replace it.
  if [[ ! -x "$DEBUG_BIN" || ! -d "$DEBUG_SPARKLE" ]]; then
    echo "ERROR: swift build did not produce $DEBUG_BIN / $DEBUG_SPARKLE — not touching $APP_TARGET." >&2
    exit 1
  fi

  # FRESHNESS, which existence alone cannot tell you (#1855). A build product
  # left over from an earlier compile — including one compiled while a
  # tools/mutate.sh mutation was applied — is byte-for-byte a valid binary and
  # passes every check above, so installing it means debugging a defect the
  # source does not have. Compare mtimes and refuse.
  if [[ ! -d "$SWIFT_SRC_DIR" ]]; then
    echo "ERROR: $SWIFT_SRC_DIR does not exist, so build freshness cannot be checked at all." >&2
    echo "       Refusing rather than installing an unverified binary into $APP_TARGET." >&2
    exit 1
  fi
  SWIFT_SRC_COUNT="$(find "$SWIFT_SRC_DIR" -name '*.swift' -type f | wc -l | tr -d ' ')"
  if [[ "$SWIFT_SRC_COUNT" -eq 0 ]]; then
    echo "ERROR: no .swift sources found under $SWIFT_SRC_DIR — the freshness check would compare" >&2
    echo "       against nothing and pass vacuously. Refusing rather than reporting a green it did not earn." >&2
    exit 1
  fi
  # `-print -quit` rather than `… -print | head -1`: the pipe is the same
  # SIGPIPE-under-pipefail trap as the codesign check below, and here it would
  # abort the script with status 141 and NO MESSAGE — so "the build is fresh"
  # and "the freshness check could not run" would look identical, which is the
  # one thing a verification mechanism must never do. Measured: the pipe form
  # aborts 0/300 against this repo's 80 sources but 40/40 at 500 files, i.e.
  # correct today only by headroom that shrinks as the target dir grows.
  STALE_SRC="$(find "$SWIFT_SRC_DIR" -name '*.swift' -type f -newer "$DEBUG_BIN" -print -quit 2>/dev/null)"
  if [[ -n "$STALE_SRC" ]]; then
    echo "ERROR: $STALE_SRC is NEWER than the built binary $DEBUG_BIN." >&2
    echo "       The build output is stale — refusing to install it into $APP_TARGET." >&2
    echo "       (Installing a binary that predates the source means debugging a defect the source no longer has.)" >&2
    exit 1
  fi

  if [[ "$MODE" == "replace" ]]; then
    # Install straight into $PROD_APP — no parallel /tmp/IrrlichtDev.app, since
    # it shares production's bundle identifier and a human only reviews one
    # running app at a time.
    if [[ ! -d "$PROD_APP" ]]; then
      echo "ERROR: $PROD_APP is not installed — run the DMG/PKG installer first." >&2
      exit 1
    fi

    # BACKUP FRESHNESS. Back up the UNTOUCHED original as a full directory copy
    # (a full-bundle backup sidesteps any code-signature mismatch between the
    # outer bundle and its nested binaries that a partial-file restore could
    # leave behind). The backup is REFRESHED, not made once-ever, whenever the
    # installed app is still an untouched release — never trust an existing
    # backup blindly, since it can predate a newer production release
    # installed since (e.g. via /ir:release), which would make restore-prod.sh
    # silently reinstall a stale build. And if the app is NOT an untouched
    # release (a prior replace-mode run's dev build, still installed) and no
    # backup exists either, REFUSE rather than overwrite the only remaining
    # copy with no safety net.
    #
    # "UNTOUCHED RELEASE" = Developer-ID-signed AND a readable
    # CFBundleShortVersionString other than $DEV_VERSION_MARKER. The signature
    # alone stopped being enough when dev builds started being signed with the
    # Developer ID identity too (step 0d): it would back a dev build up as
    # "production" on the next run and destroy the real backup. The signature
    # half stays because older dev builds — "Irrlicht Dev"/ad-hoc signed — are
    # caught by it whatever their version says. An unreadable version is NOT a
    # release: the check fails closed. PlistBuddy prints its error to STDOUT
    # with rc 1 (measured, on a missing file), so the status decides, never
    # the captured text.
    #
    # NO PIPE IN THIS CONDITION — not `codesign … | grep -q`, and not a capture
    # piped into `grep -q` either. `grep -q` exits at its first match, so
    # anything still writing to that pipe dies of SIGPIPE (141), and under
    # `set -o pipefail` the 141 IS the pipeline's status — the branch is then
    # skipped EXACTLY WHEN THE APP IS GENUINELY SIGNED. Both spellings were
    # shipped and both were wrong (#1855 review F1): the direct pipe fails
    # 5/5 against a real bundle, and capture-then-pipe only survives because
    # ~1.5 KiB fits one write into a 64 KiB pipe buffer — it is correct by
    # headroom, not by construction, and the padded fixture proves it fails.
    # Matching in-shell has no second process, so there is no race at all.
    # The leading newline anchors the match to the start of a line, which is
    # what the old `grep "^Authority=…"` did.
    CODESIGN_INFO="$(codesign -dv --verbose=4 "$PROD_APP" 2>&1 || true)"
    if ! PROD_VERSION="$("$PLISTBUDDY" -c "Print :CFBundleShortVersionString" "$PROD_APP/Contents/Info.plist" 2>/dev/null)"; then
      PROD_VERSION=""   # unreadable ⇒ not a confirmed release
    fi
    if [[ "$NL$CODESIGN_INFO" == *"${NL}Authority=Developer ID Application"* ]] \
       && [[ -n "$PROD_VERSION" && "$PROD_VERSION" != "$DEV_VERSION_MARKER" ]]; then
      rm -rf "$PROD_BACKUP"
      mkdir -p "$(dirname "$PROD_BACKUP")"
      if ! cp -R "$PROD_APP" "$PROD_BACKUP"; then
        echo "ERROR: backup of $PROD_APP to $PROD_BACKUP failed — not touching $PROD_APP." >&2
        rm -rf "$PROD_BACKUP"
        exit 1
      fi
      echo "Backed up the untouched production bundle ($PROD_VERSION) to $PROD_BACKUP (restore-prod.sh restores from this)."
    elif [[ ! -d "$PROD_BACKUP" ]]; then
      echo "ERROR: $PROD_APP is not an untouched release (that needs a Developer ID signature AND a version other than '$DEV_VERSION_MARKER'; version read: '${PROD_VERSION:-<unreadable>}') — it looks like a leftover dev build, and no backup exists — refusing to overwrite it with no safety net. Run the DMG/PKG installer first." >&2
      exit 1
    else
      echo "Keeping the existing backup at $PROD_BACKUP — $PROD_APP is a dev build (version '${PROD_VERSION:-<unreadable>}'), not a release."
    fi

    BUNDLE_DIRTIED=1   # point of no return: every failure from here owes the user the teardown hint
    # Stamp the dev marker FIRST, before any other write into the bundle, so a
    # run that fails part-way through the install never leaves a dev binary
    # under a release version — the next run would back that up as production
    # (the check above). It also keeps Settings/About from showing a stale
    # release version on a freshly compiled dev build, and matches separate
    # mode's Info.plist below so both read the same in the UI.
    "$PLISTBUDDY" -c "Set :CFBundleShortVersionString $DEV_VERSION_MARKER" "$APP_TARGET/Contents/Info.plist"
    "$PLISTBUDDY" -c "Set :CFBundleVersion $DEV_VERSION_MARKER" "$APP_TARGET/Contents/Info.plist"
    cp "$DEBUG_BIN" "$APP_TARGET/Contents/MacOS/Irrlicht"
    rm -rf "$APP_TARGET/Contents/Frameworks/Sparkle.framework"
    cp -R "$DEBUG_SPARKLE" "$APP_TARGET/Contents/Frameworks/Sparkle.framework"
    # Refresh the icon too, so a developer iterating on Resources/ assets sees
    # the same result in replace mode as in separate mode instead of a stale
    # production copy.
    cp "$SWIFT_SRC_DIR/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"
  else
    # separate — assemble a fresh /tmp/IrrlichtDev.app from scratch so
    # UNUserNotificationCenter (desktop notifications) works.
    rm -rf "$APP_TARGET"
    mkdir -p "$APP_TARGET/Contents/MacOS" "$APP_TARGET/Contents/Resources" "$APP_TARGET/Contents/Frameworks"
    cp "$DEBUG_BIN" "$APP_TARGET/Contents/MacOS/Irrlicht"
    cp "$SWIFT_SRC_DIR/Resources/AppIcon.icns" "$APP_TARGET/Contents/Resources/AppIcon.icns"
    # Embed Sparkle.framework (required since v0.4.7 auto-update integration).
    cp -R "$DEBUG_SPARKLE" "$APP_TARGET/Contents/Frameworks/"
    cat > "$APP_TARGET/Contents/Info.plist" << 'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>Irrlicht</string>
    <key>CFBundleIdentifier</key>
    <string>io.irrlicht.app</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundleName</key>
    <string>Irrlicht Dev</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>dev</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSAppleEventsUsageDescription</key>
    <string>Irrlicht uses AppleScript to bring the correct iTerm2 or Terminal.app window and tab to the front when you click a session row.</string>
    <key>NSFocusStatusUsageDescription</key>
    <string>Irrlicht uses macOS Focus status to silence notification sounds and spoken alerts while you're in Do Not Disturb, Sleep, or any other Focus mode.</string>
</dict>
</plist>
PLIST
  fi

  # The SwiftPM debug binary links Sparkle as @rpath/Sparkle.framework but only
  # carries an @loader_path rpath (= Contents/MacOS) — it lacks the bundle-layout
  # rpath a release .app build adds. Without this, dyld can't find the embedded
  # framework and the app crashes at launch. MUST run BEFORE the codesign below,
  # since it mutates the binary and invalidates the signature.
  install_name_tool -add_rpath @executable_path/../Frameworks "$APP_TARGET/Contents/MacOS/Irrlicht"

  # codesign (both modes, identical), with the identity step 0d resolved. Uses
  # the dev-only entitlements file — no com.apple.developer.* entries. Those
  # need a provisioning profile, and claiming one without it gets the launch
  # killed: v0.4.7 stripped com.apple.developer.focus-status from the release
  # for exactly that (AMFI, POSIX 153 — CHANGELOG.md), which is why the
  # release's own Irrlicht.entitlements is an empty dict too.
  # --timestamp=none: a Developer ID signature otherwise asks Apple's
  # timestamp server for a secure timestamp (measured: a default-flag sign of
  # a scratch bundle recorded `Timestamp=`, one with this flag `Signed Time=`),
  # so an offline rebuild would fail HERE — after the bundle was overwritten.
  # Dev builds are never notarized and need no secure timestamp.
  # The first Developer ID signature on a machine can raise a one-time
  # keychain prompt for codesign to use the identity's private key.
  echo "Signing $APP_TARGET with: $SIGN_LABEL …"
  codesign --force --deep --sign "$SIGN_IDENTITY" --timestamp=none --entitlements "$ENTITLEMENTS" "$APP_TARGET" 2>&1
  echo "Signed $APP_TARGET with: $SIGN_LABEL"
fi

# ─── Step 6: start the daemon, with --record for lifecycle event capture ───
# separate mode also gets IRRLICHT_PERMISSION_MODE=grant-all: a fresh isolated
# state dir has no consent answers (#570), so without it the daemon monitors
# nothing until the permission wizard is answered. Drop that variable when the
# point of the session is to test the wizard itself. replace mode omits
# IRRLICHT_HOME so it reads/writes the production state dir — including the
# user's real permission answers (no grant-all there).
if want_daemon; then
  echo "Starting irrlichd on 127.0.0.1:$PORT (log: $LOG_DIR/irrlichd-dev.log) …"
  if [[ "$MODE" == "replace" ]]; then
    IRRLICHT_BIND_ADDR="127.0.0.1:$PORT" \
      nohup "$IRRLICHTD_BIN" --record > "$LOG_DIR/irrlichd-dev.log" 2>&1 &
  else
    IRRLICHT_HOME="$DEV_HOME" IRRLICHT_BIND_ADDR="127.0.0.1:$PORT" \
      IRRLICHT_PERMISSION_MODE=grant-all \
      nohup "$IRRLICHTD_BIN" --record > "$LOG_DIR/irrlichd-dev.log" 2>&1 &
  fi
  disown 2>/dev/null || true
fi

# ─── Step 7: wait for a reachable daemon — and HARD-ABORT if there isn't one ─
# A GATE, NOT A COURTESY SLEEP. In replace mode the app adopts an already-
# reachable daemon on 7837 and skips its own spawn/pkill; if no --record daemon
# is up when the app launches, the app (port 7837 ⇒ isCustomPort false) runs
# `pkill -x irrlichd` — killing whatever daemon is there — and respawns one
# WITHOUT --record, silently defeating the whole point of the run. So if
# /state never answers, stop here and do not launch the app.
if want_daemon; then
  READY=""
  for _ in 1 2 3 4 5 6 7 8; do
    if curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1; then
      READY=1
      break
    fi
    sleep 1
  done
  if [[ -z "$READY" ]]; then
    echo "ABORT: daemon never became reachable on $PORT — not launching the app." >&2
    echo "       (Launching now would let the app pkill our daemon and respawn one without --record.)" >&2
    echo "       Check $LOG_DIR/irrlichd-dev.log." >&2
    exit 1
  fi
elif want_app; then
  # TARGET=macos: we didn't (re)start a daemon — one must already be reachable
  # for the app to adopt, or it will spawn its own (without --record, in
  # replace mode).
  if ! curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1; then
    echo "ABORT: no daemon reachable on $PORT and TARGET=macos doesn't start one." >&2
    echo "       Run with TARGET=daemon or TARGET=full first." >&2
    exit 1
  fi
fi
lsof -iTCP:"$PORT" -sTCP:LISTEN -P -n 2>/dev/null || true

# ─── Step 8: start the app ─────────────────────────────────────────────────
# Launched via LaunchServices so Bundle.main resolves correctly. In separate
# mode, IRRLICHT_DAEMON_PORT/IRRLICHT_HOME point it at the isolated dev daemon.
# In replace mode no env overrides are passed: the app uses the default port
# 7837 + production state and, finding the daemon already reachable, adopts it
# instead of spawning its own.
if want_app; then
  echo "Launching $APP_TARGET (log: $LOG_DIR/irrlicht-app-dev.log) …"
  if [[ "$MODE" == "replace" ]]; then
    open --stdout "$LOG_DIR/irrlicht-app-dev.log" --stderr "$LOG_DIR/irrlicht-app-dev.log" "$APP_TARGET"
  else
    open --env IRRLICHT_DAEMON_PORT="$PORT" --env IRRLICHT_HOME="$DEV_HOME" \
      --stdout "$LOG_DIR/irrlicht-app-dev.log" --stderr "$LOG_DIR/irrlicht-app-dev.log" "$APP_TARGET"
  fi
fi

# ─── Step 9: verify whichever components this run touched ──────────────────
echo
echo "── verify ──"
if want_daemon; then
  pgrep -f "bin/irrlichd" || true
  curl -s "http://127.0.0.1:$PORT/api/v1/sessions" | head -c 200 || true
  echo
fi
if want_app; then
  pgrep -f "$APP_KILL_PATTERN" || true
fi
if [[ "$MODE" == "replace" ]]; then
  # 0 is normal right after launch — quota data only appears once Claude Code's
  # statusline posts its next tick to 7837 (seconds to a minute). Re-run to confirm.
  QUOTA_HITS="$(curl -s "http://127.0.0.1:$PORT/api/v1/sessions" 2>/dev/null | grep -o 'rate_limit\|used_percent' | wc -l | tr -d ' ' || true)"
  echo "rate-limit mentions (0 now is fine; should climb after the next statusline tick): ${QUOTA_HITS:-0}"
  echo
  echo "Teardown when done — REQUIRED to get production back:"
  echo "  $(dirname "$0")/restore-prod.sh"
fi
