#!/usr/bin/env bash
# turn-count.sh — aider's turn counter, extracted from driver-interactive.sh.
#
# Reads the global $TRANSCRIPT and echoes the number of completed turns.
# Sourced by the driver in production, and directly by
# replaydata/_lib/drive/turn-count_test.sh — which is the whole point: the
# driver walks its recipe and calls drive_exit at source time, so this function
# was unreachable from any test while it lived there.
#
# Turn accounting is the single most defect-prone seam in the drivers — four
# separate defects in the copilot run alone, and it had no coverage at all
# (#1333 / B4). The body below is unchanged by the extraction.

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
