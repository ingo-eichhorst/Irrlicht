#!/usr/bin/env bash
# turn-count.sh — codex's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.type=="event_msg" and .payload.type=="task_complete") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}
