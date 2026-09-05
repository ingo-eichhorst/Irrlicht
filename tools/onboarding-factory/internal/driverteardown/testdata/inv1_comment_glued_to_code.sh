#!/usr/bin/env bash
# MUST BE FLAGGED (INV-1) — and before the shellsource.go fix it was NOT.
#
# `RETRY_SWEEP=1;# the driver's own retry flag` glues a comment to code. The
# apostrophe in "driver's" sits INSIDE that comment, but the line-joiner and the
# word scanner did not share a `#`-is-a-comment rule: the joiner only accepted a
# `#` at column 0 or after a space/tab, so it read the apostrophe as an
# unterminated quote and joined the rest of the file onto this line, and the word
# scanner then truncated the joined text at the `#`. Every statement below
# vanished — the gated top-level `tmux kill-session` included — while `sites`
# stayed non-zero, so INV-1 reported this driver CLEAN and the vacuity guard had
# nothing to catch.
set -euo pipefail
_DRIVE_LIB="$(dirname "${BASH_SOURCE[0]}")/lib"
source "$_DRIVE_LIB/slots.sh"

SESSION=""
SES_SESSION=()
SES_CWD=()
SES_OWNED=()
N_SLOTS=0
ACTIVE=0
EXIT_REASON="ok"
REACHED_EPILOGUE=0
RUN_CWD="$STAGING/cwd"

cleanup() {
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
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

launch_repl

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1

RETRY_SWEEP=1;# the driver's own retry flag
if [[ "${SES_OWNED[$ACTIVE]:-0}" == "1" ]]; then
  tmux kill-session -t "$SESSION" 2>/dev/null || true
fi
