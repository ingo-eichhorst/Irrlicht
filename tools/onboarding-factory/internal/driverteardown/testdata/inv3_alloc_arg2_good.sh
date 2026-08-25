#!/usr/bin/env bash
# GOOD, claudecode's private slot scheme: alloc_slot takes (uuid, tmux-name,
# cwd) and the name in argument TWO carries $$ as its own field. Paired with
# inv3_alloc_arg2_bad.sh so the position-2 resolution is shown to grade BOTH
# ways rather than merely never firing.
set -euo pipefail
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots_pos2.sh"

CURRENT_TMUX=""
SES_TMUX=()
SES_UUID=()
SES_CWD=()
N_SLOTS=0
ACTIVE=0
UUID="00000000-0000-0000-0000-000000000000"
RUN_CWD="$STAGING/cwd"

cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_TMUX[$i]:-}" ]] && tmux kill-session -t "${SES_TMUX[$i]}" 2>/dev/null || true
  done
}
trap cleanup EXIT

alloc_slot "$UUID" "fixture-onboard-$(date +%s)-$$" "$RUN_CWD"
tmux new-session -d -s "$CURRENT_TMUX" -c "$CURRENT_CWD" -- "fixture-agent"
