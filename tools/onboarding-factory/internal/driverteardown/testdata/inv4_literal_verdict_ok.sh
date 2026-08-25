#!/usr/bin/env bash
# MUST NOT BE FLAGGED — INV-4's precondition is DERIVED, and this handler does
# not meet it. It writes a literal fault verdict rather than a variable, so
# there is no initial value for the write to be stale at: every abort path
# records the same explicit fault, and the epilogue writes the real verdict
# itself. Exempt because of what the source says, not because of a list.
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
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "nonzero(2)" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

launch_repl() {
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

step_exit_clean() {
  tmux send-keys -t "$SESSION" C-d
  SES_ALIVE[$ACTIVE]=0
}

launch_repl
step_exit_clean

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

# The epilogue writes the real verdict itself, over whatever the trap recorded.
echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
