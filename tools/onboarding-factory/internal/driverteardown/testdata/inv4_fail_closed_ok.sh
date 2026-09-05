#!/usr/bin/env bash
# MUST NOT BE FLAGGED — a driver that legitimately writes a verdict it did
# form, by the OTHER correct design. EXIT_REASON starts at a fault and the
# epilogue promotes it to `ok` on the success path, so an unconditional handler
# write is already honest: an abort writes `nonzero(2)`, a completed run writes
# `ok`, and the two are different bytes. There is no sentinel and no guard.
#
# This row is what stops INV-4 prescribing one implementation. A rule that
# demanded aider's guard would fail this driver, which is strictly correct.
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="nonzero(2)"
RUN_CWD="$STAGING/cwd"

cleanup() {
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

# The epilogue completed with no fault: promote the fail-closed initialiser to
# this run's real verdict. cleanup() writes whatever is here.
EXIT_REASON="ok"
