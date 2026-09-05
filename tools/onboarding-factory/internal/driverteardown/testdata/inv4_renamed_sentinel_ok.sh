#!/usr/bin/env bash
# MUST NOT BE FLAGGED — the sentinel idiom spelled with a different variable
# name throughout. INV-4 matches no variable name at all: it looks for an
# unconditional top-level assignment placed after the trap arms and after the
# last `tmux new-session`, which is what "the epilogue set it" looks like with
# the names removed. If this row ever went red, the rule would have quietly
# become a check on aider's spelling rather than on the property.
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
DRIVER_RAN_TO_COMPLETION=0
RUN_CWD="$STAGING/cwd"

cleanup() {
  # The epilogue sets DRIVER_RAN_TO_COMPLETION immediately before the final exit, so a
  # handler that sees it unset knows the script aborted before forming a
  # verdict and must not write EXIT_REASON's initialiser as one (INV-4).
  if [[ "$DRIVER_RAN_TO_COMPLETION" != "1" && "$EXIT_REASON" == "ok" ]]; then
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
DRIVER_RAN_TO_COMPLETION=1
