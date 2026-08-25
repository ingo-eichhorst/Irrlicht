#!/usr/bin/env bash
# VACUITY — launches a tmux session and never kills one anywhere. INV-1 has no
# teardown site to grade here, and "every teardown site is ungated" must not be
# the answer a driver with NO teardown site gets: absence of a finding and
# inability to look have to be different outputs. This must ERROR, not pass.
set -euo pipefail
SESSION="fixture-onboard-$(date +%s)-$$"
RUN_CWD="$STAGING/cwd"

tmux new-session -d -s "$SESSION" -c "$RUN_CWD" "fixture-agent"
echo "ok" > "$STAGING/driver.exit-reason"
