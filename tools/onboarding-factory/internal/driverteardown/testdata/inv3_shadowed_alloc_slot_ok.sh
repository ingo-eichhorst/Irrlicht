#!/usr/bin/env bash
# MUST NOT BE FLAGGED — the shadowed-`alloc_slot` collision.
#
# This driver defines its OWN alloc_slot, whose tmux session name is the SECOND
# argument (claudecode's private slot scheme). It does NOT source slots.sh — but
# LoadDriver globs the whole _lib/drive directory and hands every adapter every
# file in it, so the shared alloc_slot, whose name is the FIRST argument, is
# parsed alongside this driver regardless.
#
# The line that lights the collision is `SESSION="$CURRENT_TMUX"` below: it
# makes SESSION name-carrying, slots.sh assigns SESSION="$sess", so slots.sh's
# `local sess="$1"` marks argument position 1 as name-carrying — under a
# `pos` map keyed by BARE FUNCTION NAME, on the same key this driver's own
# alloc_slot uses for position 2. Both positions end up marked, and the literal
# at position 1 is graded as a session name.
set -euo pipefail

SESSION=""
SES_UUID=()
SES_TMUX=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
CURRENT_UUID=""
CURRENT_TMUX=""
EXIT_REASON="ok"
REACHED_EPILOGUE=0
RUN_CWD="$STAGING/cwd"

alloc_slot() {
  N_SLOTS=$((N_SLOTS + 1))
  SES_UUID[$N_SLOTS]="$1"
  SES_TMUX[$N_SLOTS]="$2"
  SES_CWD[$N_SLOTS]="$3"
  SES_OWNED[$N_SLOTS]=1
  ACTIVE=$N_SLOTS
  CURRENT_UUID="$1"
  CURRENT_TMUX="$2"
}

cleanup() {
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
  fi
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_TMUX[$i]:-}" ]] && tmux kill-session -t "${SES_TMUX[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

launch_repl() {
  alloc_slot "fixture-uuid-0000" "fixture-onboard-$(date +%s)-$$-r${ACTIVE}" "$RUN_CWD"
  # Legacy alias every step below still reads.
  SESSION="$CURRENT_TMUX"
  tmux new-session -d -s "$CURRENT_TMUX" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

launch_repl

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_TMUX[$i]:-}" ]]; then
    tmux kill-session -t "${SES_TMUX[$i]}" 2>/dev/null || true
  fi
done

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
