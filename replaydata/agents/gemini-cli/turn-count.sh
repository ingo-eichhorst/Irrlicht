#!/usr/bin/env bash
# turn-count.sh — gemini-cli's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  [[ -f "$TRANSCRIPT" ]] || { echo 0; return; }
  jq -r 'select(.type=="gemini"
                and ((.content // "") | gsub("\\s";"") | length) > 0
                and ((.toolCalls // []) | length) == 0) | "x"' \
    "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
}
