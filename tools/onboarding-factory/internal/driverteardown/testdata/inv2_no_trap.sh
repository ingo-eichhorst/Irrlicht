#!/usr/bin/env bash
# MUTATION — the committed proof that INV-2 goes red when a driver installs no
# EXIT trap at all. This is aider's, and pre-#1825 claudecode's/codex's/pi's,
# shape: the final kill-session is reached only when the script runs to the
# bottom, so any `set -e` abort or early `exit` leaves the pane alive.
set -euo pipefail
SESSION="fixture-onboard-$(date +%s)-$$"
RUN_CWD="$STAGING/cwd"

tmux kill-session -t "$SESSION" 2>/dev/null || true
tmux new-session -d -s "$SESSION" -c "$RUN_CWD" "fixture-agent"

echo "[driver] running" >&2

tmux kill-session -t "$SESSION" 2>/dev/null || true
echo "ok" > "$STAGING/driver.exit-reason"
