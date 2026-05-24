#!/usr/bin/env bash
# restore-prod.sh — return to the installed production Irrlicht after a
# `replace`-mode dev session (see SKILL.md step 9).
#
# Why a dedicated script: quitting the dev app is NOT enough. In replace mode
# the app only *adopted* the nohup/disown'd dev `--record` daemon on 7837
# (DaemonManager.start() returns early on a reachable daemon and never records
# the process, so terminateProcess() is a no-op). That daemon therefore
# survives the app's exit, and a relaunched production app finds it still
# reachable on 7837 and silently adopts it — you'd be running the production UI
# against the dev `--record` daemon. This script kills both, GATES on the port
# actually being free, then launches production so it spawns its OWN bundled
# daemon.
#
# Usage:  .claude/skills/ir:test-mac/restore-prod.sh
set -euo pipefail

PROD_APP="/Applications/Irrlicht.app"
PORT=7837

echo "Stopping dev app + daemon…"
pkill -f "IrrlichtDev" 2>/dev/null || true   # the dev app
pkill -x "irrlichd"    2>/dev/null || true   # the adopted dev --record daemon (the app won't)

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

if [ ! -d "$PROD_APP" ]; then
  echo "ERROR: $PROD_APP is not installed — run the DMG/PKG installer first, then re-run this script." >&2
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
