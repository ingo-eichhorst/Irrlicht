#!/usr/bin/env bash
# MUST BE FLAGGED (INV-2) AND MUST REFUSE (INV-1 graded nothing) — both at once,
# and the INV-2 finding is the one that names the defect.
#
# hermes's shape with `trap cleanup EXIT` deleted. cleanup() still exists and
# still kills, but nothing arms it, and hermes has no top-level end-of-run sweep
# — the trap is its ONLY teardown. So INV-1 has no site to grade and refuses,
# and a CheckDriver that returned `nil, err` threw the INV-2 finding away: the
# gate went red naming INV-1, which is the consequence, not the cause.
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
RUN_CWD="$STAGING/cwd"

cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
}

launch_repl() {
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

step_exit_clean() {
  tmux send-keys -t "$SESSION" C-d
  SES_ALIVE[$ACTIVE]=0
}

launch_repl
step_exit_clean
