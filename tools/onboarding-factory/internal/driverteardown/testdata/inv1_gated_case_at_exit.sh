#!/usr/bin/env bash
# MUST BE FLAGGED (INV-1) — the other side of the step-dispatch narrowing.
#
# A top-level `case` that is NOT inside a loop is not a step dispatch; it is the
# trailing `case "$EXIT_REASON" in` shape aider, claudecode and opencode already
# write at the bottom of their drivers. A liveness-gated kill in one of those
# arms is end-of-run teardown and must still be flagged, so the narrowing cannot
# be "a `case` arm is never teardown".
set -euo pipefail
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
REACHED_EPILOGUE=0
RUN_CWD="$STAGING/cwd"

cleanup() {
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
  fi
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

launch_repl() {
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

launch_repl

case "$EXIT_REASON" in
  ok)  if [[ "${SES_ALIVE[$ACTIVE]:-0}" == "1" ]]; then
         tmux kill-session -t "$SESSION" 2>/dev/null || true
       fi ;;
  *)   tmux kill-session -t "$SESSION" 2>/dev/null || true ;;
esac

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
