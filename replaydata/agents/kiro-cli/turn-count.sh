#!/usr/bin/env bash
# turn-count.sh — kiro-cli's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.kind=="AssistantMessage")
           | select([.data.content[]? | select(.kind=="toolUse")] | length == 0)
           | "x"' "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}
