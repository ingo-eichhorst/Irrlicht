#!/usr/bin/env bash
# MUST NOT BE FLAGGED, AND MUST PARSE — mistral-vibe's shape, and the reason the
# line-joiner exists at all. The `awk '…'` program spans lines, its braces are
# DATA rather than shell blocks, and it carries awk comments of its own. Without
# the join the braces are counted as blocks and the whole file is reported
# unreadable; with a `#` rule the joiner and the word scanner do not share, an
# awk comment could end the join early. This is a LOCK: it passed before the
# shellsource.go fix and must keep passing after it.
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

turn_count() {
  tmux capture-pane -p -t "$SESSION" | awk '
    # awk comment, inside the quotes: the braces below are DATA
    /^> / { n += 1 }
    END   { print n + 0 }
  '
}

launch_repl() {
  alloc_slot "fixture-onboard-$(date +%s)-$$-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "fixture-agent"
}

launch_repl
turn_count

# Tear down every session this run created.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -n "${SES_SESSION[$i]:-}" ]]; then
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

# The epilogue completed: EXIT_REASON is this run's real verdict.
REACHED_EPILOGUE=1
