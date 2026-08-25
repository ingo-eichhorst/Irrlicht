#!/usr/bin/env bash
# VACUITY — `tmux new-session` with no `-s`, so tmux names the session itself.
# The name is then outside the driver's control and cannot carry its PID, which
# is precisely the state INV-3 exists to forbid. Reading it as "no name to
# grade" would let it pass, so this must ERROR.
set -euo pipefail
RUN_CWD="$STAGING/cwd"

cleanup() {
  tmux kill-session -t fixture 2>/dev/null || true
}
trap cleanup EXIT

tmux new-session -d -c "$RUN_CWD" "fixture-agent"
