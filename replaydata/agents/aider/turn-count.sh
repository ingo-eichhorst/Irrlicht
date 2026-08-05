#!/usr/bin/env bash
# turn-count.sh — aider's turn counter, sourced by driver-interactive.sh.
# Extracted so it can be unit-tested without executing the driver, which walks
# its recipe at source time. Reads $TRANSCRIPT, echoes the completed-turn count.
# Rationale + the four defects this seam has had: replaydata/_lib/drive/turn-count_test.sh

turn_count() {
  # `grep -c` always prints the count to stdout; on no-match it also exits
  # non-zero. Swallow that exit so `|| echo 0` doesn't run and double the
  # output to "0\n0", which then breaks `[[ $now -gt $before ]]`.
  if [[ -f "$TRANSCRIPT" ]]; then
    grep -c '^> Tokens:' "$TRANSCRIPT" 2>/dev/null || true
  else
    echo 0
  fi
}
