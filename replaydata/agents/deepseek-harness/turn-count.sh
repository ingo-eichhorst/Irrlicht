#!/usr/bin/env bash
# turn-count.sh — DeepSeek Harness completed-turn counter.
# The driver sources this file so the shared turn-count suite can test the
# counter without starting the TUI. Reads $TRANSCRIPT and prints one integer.

turn_count() {
  if [[ ! -s "$TRANSCRIPT" ]]; then
    echo 0
    return 0
  fi

  local count
  if ! count="$(zstd -qdc "$TRANSCRIPT" 2>/dev/null \
      | jq -c 'select(.type == "turn/end")' 2>/dev/null \
      | wc -l \
      | tr -d ' ')"; then
    return 2
  fi
  printf '%s\n' "$count"
}
