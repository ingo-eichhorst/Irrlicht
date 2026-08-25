#!/usr/bin/env bash
# MUTATION of good_driver.sh — the committed regression fixture for a FALSE
# PASS this checker actually had, caught by measuring rather than by reasoning.
#
# INV-4's second shape (fail-closed) asks whether the verdict variable is
# re-assigned by an UNCONDITIONAL TOP-LEVEL statement after the trap arms. The
# first implementation read "unconditional" as "no enclosing `if`", and every
# one of the eleven shipped drivers has an `EXIT_REASON="nonzero(2)"` inside a
# `case` arm in its step loop — no `if` anywhere near it. So all ten bound
# drivers passed INV-4 on a fail-closed reading they had not earned, and the
# guard branch was never reached. The invariant was live and grading nothing.
#
# This fixture is that shape with the guard removed: the ONLY re-assignment of
# EXIT_REASON after the trap is inside a `case` arm, which runs on some paths
# and not others, so an abort still writes the initialiser. It must be RED.
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
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

# The step loop's fault arm. It is at top level and has no enclosing `if`, but
# it runs only on the paths that reach it — it is not the epilogue.
case "$STEP_KIND" in
  known)   : ;;
  *)       EXIT_REASON="nonzero(2)" ;;
esac
