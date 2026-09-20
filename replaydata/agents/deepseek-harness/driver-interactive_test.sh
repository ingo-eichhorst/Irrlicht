#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DRIVER="$ROOT/replaydata/agents/deepseek-harness/driver-interactive.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Load only the polling function. The production driver otherwise starts tmux.
# shellcheck disable=SC2046
eval "$(sed -n '/^step_wait_compaction() {/,/^}/p' "$DRIVER")"

resolve_transcript() { :; }
sleep() { :; }
remaining_seconds() { echo "${REMAINING:-1}"; }

run_success() {
  printf '%s\n' '{"type":"compaction/end"}' | zstd -q -c > "$TMP/success.zstd"
  TRANSCRIPT="$TMP/success.zstd" ACTIVE=1 EXIT_REASON="ok" REMAINING=1
  step_wait_compaction
}

run_absent() {
  printf '%s\n' '{"type":"assistant/message"}' | zstd -q -c > "$TMP/absent.zstd"
  TRANSCRIPT="$TMP/absent.zstd" ACTIVE=1 EXIT_REASON="ok" REMAINING=0
  if step_wait_compaction; then
    echo "absent marker unexpectedly succeeded" >&2
    return 1
  fi
  [[ "$EXIT_REASON" == "timeout" ]]
}

run_unreadable() {
  printf 'not zstd\n' > "$TMP/bad.zstd"
  printf '100\n' > "$TMP/clock"
  date() { local now; now="$(<"$TMP/clock")"; echo "$((now + 11))" > "$TMP/clock"; echo "$now"; }
  TRANSCRIPT="$TMP/bad.zstd" ACTIVE=1 EXIT_REASON="ok" REMAINING=1
  if step_wait_compaction; then
    echo "unreadable transcript unexpectedly succeeded" >&2
    return 1
  fi
  [[ "$EXIT_REASON" == "unreadable_transcript" ]]
}

run_success
run_absent
run_unreadable
echo "ok: deepseek-harness wait_compaction"
