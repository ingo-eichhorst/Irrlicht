#!/usr/bin/env bash
# turn-count.sh — copilot's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  local t="${TRANSCRIPT:-}"
  [[ -n "$t" && -f "$t" ]] || { echo 0; return; }
  # grep -c PRINTS 0 and EXITS 1 when there is no match, so a `|| echo 0`
  # fallback emitted two lines ("0\n0") and every (( )) comparison downstream
  # died with a syntax error. Count with awk instead: one line, always.
  awk '
    /"agentId":/                     { next }
    /"type":"user\.message"/         { prompts++; open = 1 }
    /"type":"assistant\.turn_start"/ { open = 1 }
    /"type":"assistant\.turn_end"/   { open = 0 }
    END { print (prompts - open) + 0 }
  ' "$t"
}
