#!/usr/bin/env bash
# MUTATION — the committed proof that INV-3 goes red for a session name that
# carries no driver PID. run-cell.sh keeps no record of the names a driver
# chose, so a name without $$ cannot be attributed to the run that made it and
# the post-run leak assertion has nothing to match on.
set -euo pipefail
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
RUN_CWD="$STAGING/cwd"

cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
}
trap cleanup EXIT

alloc_slot "fixture-onboard-$(date +%s)" "$RUN_CWD"
tmux new-session -d -s "$SESSION" -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
