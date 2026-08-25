#!/usr/bin/env bash
# MUTATION — the committed proof that INV-2 goes red for a trap that ARMS but
# tears nothing down. A trap that only writes driver.exit-reason satisfies a
# grep for `trap … EXIT` while leaving exactly the leak INV-2 exists to stop,
# so "there is a trap" and "the trap cleans up" must be different answers.
set -euo pipefail
SESSION="fixture-onboard-$(date +%s)-$$"
RUN_CWD="$STAGING/cwd"
EXIT_REASON="ok"
REACHED_EPILOGUE=0

cleanup() {
  # The epilogue sets REACHED_EPILOGUE immediately before the final exit, so a
  # handler that sees it unset knows the script aborted before forming a
  # verdict and must not write EXIT_REASON's initialiser as one (INV-4).
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
  fi
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

tmux new-session -d -s "$SESSION" -c "$RUN_CWD" "fixture-agent"

tmux kill-session -t "$SESSION" 2>/dev/null || true

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
