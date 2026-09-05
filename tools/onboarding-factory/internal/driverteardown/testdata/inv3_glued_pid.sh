#!/usr/bin/env bash
# MUTATION — the committed proof that INV-3 goes red for a $$ that is present
# but GLUED to another token instead of standing as its own `-`-delimited
# field. run-cell.sh splits a session name on `-` and compares fields, so
# `fixture-onboard12345-...` does not match pid 12345 — and worse, a name
# ending in a longer number whose tail happens to be the pid would match a
# DIFFERENT run's. Substring-matching a pid is not attribution.
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

alloc_slot "fixture-onboard$$-$(date +%s)" "$RUN_CWD"
tmux new-session -d -s "$SESSION" -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
