#!/usr/bin/env bash
# turn-count.sh — antigravity's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  [[ -f "$TRANSCRIPT" ]] || { echo 0; return; }
  jq -r 'select(.source=="MODEL" and .type=="PLANNER_RESPONSE"
                and ((.tool_calls // []) | length) == 0) | "x"' \
    "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
}
