#!/usr/bin/env bash
# restore-prod.sh — return to the real production Irrlicht after a
# `replace`-mode dev session (see SKILL.md step 10).
#
# Why a dedicated script: quitting the dev app is NOT enough, for two
# independent reasons.
#   1. The daemon: DaemonManager.start() adopts an already-reachable daemon
#      without ever recording the process, so terminateProcess() is a no-op —
#      the nohup/disown'd dev --record daemon on 7837 survives the app's
#      exit, and a relaunched production app would silently adopt it.
#   2. The app itself (#833): replace mode installs the dev build DIRECTLY
#      into /Applications/Irrlicht.app instead of a separate bundle, so its
#      executable + Sparkle.framework may have been overwritten with the dev
#      build. Relaunching that bundle would launch the dev binary, not
#      production, until the untouched original is restored.
#
# This script kills both, restores the full backed-up production bundle (if
# a dev overwrite ever happened — a full-directory swap, not a partial-file
# copy, so the outer bundle's code signature is never left inconsistent with
# its nested binaries), GATES on the port actually being free, then launches
# production so it spawns its OWN bundled daemon.
#
# Usage:  .claude/skills/ir:test-mac/restore-prod.sh
set -euo pipefail

PROD_APP="/Applications/Irrlicht.app"
PROD_BACKUP="/Users/ingo/projects/irrlicht/.build/irrlicht-prod-backup/Irrlicht.app"
PORT=7837

APP_PATTERN="Irrlicht\.app/Contents/MacOS/Irrlicht"

echo "Stopping dev app + daemon…"
pkill -f "$APP_PATTERN" 2>/dev/null || true   # the app (production or dev-overwritten)
pkill -f "IrrlichtDev"  2>/dev/null || true   # any leftover separate-mode dev app
pkill -x "irrlichd"     2>/dev/null || true   # the adopted dev --record daemon (the app won't)

# pkill is async — wait for the process to actually exit before touching its
# on-disk bundle below (rm -rf/cp on a still-running app's executable can
# corrupt the copy or crash the dying process mid-teardown).
for _ in 1 2 3 4 5; do
  pgrep -f "$APP_PATTERN" >/dev/null 2>&1 || break
  sleep 1
done
if pgrep -f "$APP_PATTERN" >/dev/null 2>&1; then
  echo "ERROR: the app process is still running after teardown — refusing to touch its bundle." >&2
  exit 1
fi

# pkill is async — wait for the listener to actually disappear before deciding.
for _ in 1 2 3 4 5; do
  lsof -iTCP:"$PORT" -sTCP:LISTEN -P -n >/dev/null 2>&1 || break
  sleep 1
done

if lsof -iTCP:"$PORT" -sTCP:LISTEN -P -n >/dev/null 2>&1; then
  echo "ERROR: port $PORT still held after teardown — refusing to launch production." >&2
  echo "       (Production would adopt whatever daemon is on $PORT.) Current holder:" >&2
  lsof -iTCP:"$PORT" -sTCP:LISTEN -P -n >&2 || true
  exit 1
fi
echo "Port $PORT free."

if [[ -d "$PROD_BACKUP" ]]; then
  echo "Restoring the untouched production bundle from $PROD_BACKUP ..."
  # Copy to a staging path first and swap in with mv, so a failed/interrupted
  # copy never leaves $PROD_APP deleted with nothing in its place.
  rm -rf "$PROD_APP.restoring"
  cp -R "$PROD_BACKUP" "$PROD_APP.restoring"
  rm -rf "$PROD_APP"
  mv "$PROD_APP.restoring" "$PROD_APP"
elif [[ ! -d "$PROD_APP" ]]; then
  echo "ERROR: $PROD_APP is not installed and no backup exists at $PROD_BACKUP." >&2
  echo "       Run the DMG/PKG installer first, then re-run this script." >&2
  exit 1
elif codesign -dv --verbose=4 "$PROD_APP" 2>&1 | grep -q "^Authority=Developer ID Application"; then
  echo "No backup at $PROD_BACKUP — $PROD_APP is already genuinely production-signed; nothing to restore."
else
  echo "ERROR: $PROD_APP is not Developer-ID-signed (looks like a leftover dev build) and no backup exists at $PROD_BACKUP." >&2
  echo "       Cannot confirm production is intact — reinstall via the DMG/PKG installer." >&2
  exit 1
fi

echo "Launching production $PROD_APP …"
open "$PROD_APP"

# Production spawns its own bundled daemon (no --record) now that 7837 is free.
for _ in 1 2 3 4 5 6 7 8; do
  curl -fsS --max-time 1 "http://127.0.0.1:$PORT/state" >/dev/null 2>&1 && {
    echo "Production daemon reachable on $PORT — restored."
    exit 0
  }
  sleep 1
done
echo "WARNING: production launched but daemon not reachable on $PORT yet — check the menu-bar app." >&2
