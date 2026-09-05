#!/usr/bin/env bash
# MUTATION of good_driver.sh — the committed proof that INV-4 grades the VALUE
# the guard produces, not merely that a guard ran. The sentinel is real and the
# epilogue sets it, but the guard assigns EXIT_REASON its own initialiser, so
# the bytes written on an abort are identical to the bytes written by a
# completed run. A no-op guard is not a guard.
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
REACHED_EPILOGUE=0
RUN_CWD="$STAGING/cwd"

cleanup() {
  # The epilogue sets REACHED_EPILOGUE immediately before the final exit, so a
  # handler that sees it unset knows the script aborted before forming a
  # verdict and must not write EXIT_REASON's initialiser as one (INV-4).
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="ok"
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
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

step_exit_clean() {
  tmux send-keys -t "$SESSION" C-d
  SES_OWNED[$ACTIVE]=0
}

launch_repl
step_exit_clean

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
