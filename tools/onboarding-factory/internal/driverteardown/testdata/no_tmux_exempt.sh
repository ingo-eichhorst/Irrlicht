#!/usr/bin/env bash
# EXEMPT — a headless driver that launches no tmux session at all. The
# exemption is DERIVED (no `tmux new-session` in the file), never listed, so an
# adapter that adds a tmux launch tomorrow starts being graded on that edit
# rather than on someone remembering this package. The non-empty check is what
# keeps this from also covering "the file could not be read".
set -euo pipefail
SESSION_ID=""
RUN_CWD="$STAGING/cwd"

send_prompt() {
  fixture-agent run --cwd "$RUN_CWD" --session "$SESSION_ID" "$1"
}

send_prompt "hello"
echo "ok" > "$STAGING/driver.exit-reason"
