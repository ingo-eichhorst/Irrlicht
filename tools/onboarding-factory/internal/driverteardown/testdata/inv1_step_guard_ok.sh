#!/usr/bin/env bash
# MUST NOT BE FLAGGED — kiro-cli's shape, and the reason INV-1 is structural
# rather than textual.
#
# step_exit_clean and step_sigkill open with an SES_ALIVE guard AND kill a
# session inside it. Those guards are CORRECT: reset_session hands a new slot
# the SAME tmux pane as an old one, so an already-retired slot NUMBER can alias
# a pane a different, still-live slot now owns, and a recipe re-targeting the
# old number must not tear the live session down.
#
# Teardown itself — the trap handler and the end-of-run sweep — is ungated, so
# the file is clean. A rule keyed on "SES_ALIVE near a kill-session" would fail
# this driver, and a finding against a driver that is right is the kind that
# gets a rule ignored rather than fixed.
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
  # The epilogue sets REACHED_EPILOGUE immediately before the final exit, so a
  # handler that sees it unset knows the script aborted before forming a
  # verdict and must not write EXIT_REASON's initialiser as one (INV-4).
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
  alloc_slot "fixture-onboard-$(date +%s)-$$-r${ACTIVE}" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

step_exit_clean() {
  if [[ "${SES_ALIVE[$ACTIVE]:-0}" != "1" ]]; then
    echo "[driver] exit_clean[s$ACTIVE]: slot already retired -- refusing" >&2
    return 0
  fi
  tmux send-keys -t "$SESSION" C-d
  SES_ALIVE[$ACTIVE]=0
}

step_sigkill() {
  if [[ "${SES_ALIVE[$ACTIVE]:-0}" == "1" ]]; then
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    SES_ALIVE[$ACTIVE]=0
  fi
}

launch_repl
step_exit_clean
step_sigkill

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
