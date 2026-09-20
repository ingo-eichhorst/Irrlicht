#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DRIVER="$ROOT/replaydata/agents/deepseek-harness/driver-interactive.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Load only the polling function. The production driver otherwise starts tmux.
# shellcheck disable=SC2046
eval "$(sed -n '/^step_wait_compaction() {/,/^}/p' "$DRIVER")"
eval "$(sed -n '/^stop_session_updates_process() {/,/^}/p' "$DRIVER")"
eval "$(sed -n '/^step_capture_session_updates() {/,/^}/p' "$DRIVER")"
eval "$(sed -n '/^step_stop_session_updates() {/,/^}/p' "$DRIVER")"

resolve_transcript() { :; }

reset_remaining() {
  remaining_seconds() { echo "${REMAINING:-1}"; }
}
sleep() { :; }
remaining_seconds() { echo "${REMAINING:-1}"; }

run_success() {
  printf '%s\n' '{"type":"compaction/end"}' | zstd -q -c > "$TMP/success.zstd"
  TRANSCRIPT="$TMP/success.zstd" ACTIVE=1 EXIT_REASON="ok" REMAINING=1
  step_wait_compaction
}

run_absent() {
  printf '%s\n' '{"type":"assistant/message"}' | zstd -q -c > "$TMP/absent.zstd"
  rm -f "$TMP/absent-polled"
  remaining_seconds() { if [[ ! -e "$TMP/absent-polled" ]]; then : > "$TMP/absent-polled"; echo 1; else echo 0; fi; }
  TRANSCRIPT="$TMP/absent.zstd" ACTIVE=1 EXIT_REASON="ok"
  if step_wait_compaction; then
    echo "absent marker unexpectedly succeeded" >&2
    return 1
  fi
  [[ "$EXIT_REASON" == "timeout" ]]
}

run_malformed_json() {
  printf 'not json\n' | zstd -q -c > "$TMP/malformed.zstd"
  printf '100\n' > "$TMP/clock"
  date() { local now; now="$(<"$TMP/clock")"; echo "$((now + 11))" > "$TMP/clock"; echo "$now"; }
  remaining_seconds() { echo 1; }
  TRANSCRIPT="$TMP/malformed.zstd" ACTIVE=1 EXIT_REASON="ok"
  if step_wait_compaction; then
    echo "malformed JSON unexpectedly succeeded" >&2
    return 1
  fi
  [[ "$EXIT_REASON" == "unreadable_transcript" ]]
}

run_unreadable() {
  printf 'not zstd\n' > "$TMP/bad.zstd"
  printf '100\n' > "$TMP/clock"
  date() { local now; now="$(<"$TMP/clock")"; echo "$((now + 11))" > "$TMP/clock"; echo "$now"; }
  remaining_seconds() { echo 1; }
  # shellcheck disable=SC2034 # eval-loaded driver function reads these globals.
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
run_malformed_json

start_capture_process() {
  /bin/sleep 60 &
  # shellcheck disable=SC2034 # eval-loaded driver function reads this global.
  UPDATES_CAPTURE_PID=$!
}

resolve_transcript() { :; }

run_capture_matching_frame() {
  reset_remaining
  STAGING="$TMP/capture-matching" UUID="session-right" EXIT_REASON="ok" REMAINING=1
  mkdir -p "$STAGING"
  printf '%s\n' \
    '{"type":"session_updated","session":{"session_id":"session-wrong"}}' \
    '{"type":"session_updated","session":{"session_id":"session-right","state":"working","task_estimate":4}}' \
    '{"type":"session_updated","session":{"session_id":"session-right","state":"ready"}}' > "$STAGING/session_updates.raw.jsonl"
  start_capture_process
  step_stop_session_updates
  [[ "$(wc -l < "$STAGING/session_updates.jsonl" | tr -d ' ')" == "2" ]]
  jq -e 'select(.session.state == "working" and .session.task_estimate == 4)' "$STAGING/session_updates.jsonl" >/dev/null
  jq -e 'select(.session.state == "ready" and (.session.task_estimate | not))' "$STAGING/session_updates.jsonl" >/dev/null
}

run_capture_zero_or_wrong_frames() {
  reset_remaining
  STAGING="$TMP/capture-wrong" UUID="session-right" EXIT_REASON="ok" REMAINING=0
  mkdir -p "$STAGING"
  printf '%s\n' '{"type":"session_updated","session":{"session_id":"session-wrong"}}' > "$STAGING/session_updates.raw.jsonl"
  start_capture_process
  if step_stop_session_updates; then
    echo "wrong-session frame unexpectedly succeeded" >&2
    return 1
  fi
  stop_session_updates_process
  [[ "$EXIT_REASON" == "capture_ready_timeout" ]]
}

run_capture_malformed_json() {
  reset_remaining
  STAGING="$TMP/capture-malformed" UUID="session-right" EXIT_REASON="ok" REMAINING=1
  mkdir -p "$STAGING"
  printf '%s\n' \
    '{"type":"session_updated","session":{"session_id":"session-right"}}' \
    'not json' > "$STAGING/session_updates.raw.jsonl"
  start_capture_process
  if step_stop_session_updates; then
    echo "malformed session-update frame unexpectedly succeeded" >&2
    return 1
  fi
  stop_session_updates_process
  [[ "$EXIT_REASON" == "capture_unreadable" ]]
  [[ ! -e "$STAGING/session_updates.jsonl" ]]
}

run_capture_readiness_failure() {
  reset_remaining
  # shellcheck disable=SC2034 # eval-loaded driver function reads this global.
  STAGING="$TMP/capture-unready" EXIT_REASON="ok" CAPTURE_READY_TIMEOUT_S=0
  mkdir -p "$STAGING/bin"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$STAGING/bin/node"
  chmod +x "$STAGING/bin/node"
  local old_path="$PATH"
  PATH="$STAGING/bin:$PATH"
  if step_capture_session_updates; then
    echo "unready capture unexpectedly succeeded" >&2
    PATH="$old_path"
    return 1
  fi
  PATH="$old_path"
  [[ "$EXIT_REASON" == "capture_unready" ]]
}

run_capture_ready_frame_timeout() {
  # shellcheck disable=SC2034 # eval-loaded driver function reads this global.
  STAGING="$TMP/capture-no-ready" UUID="session-right" EXIT_REASON="ok"
  mkdir -p "$STAGING"
  printf '%s\n' '{"type":"session_updated","session":{"session_id":"session-right","state":"working"}}' > "$STAGING/session_updates.raw.jsonl"
  rm -f "$TMP/no-ready-polled"
  remaining_seconds() { if [[ ! -e "$TMP/no-ready-polled" ]]; then : > "$TMP/no-ready-polled"; echo 1; else echo 0; fi; }
  start_capture_process
  if step_stop_session_updates; then
    echo "missing ready frame unexpectedly succeeded" >&2
    return 1
  fi
  stop_session_updates_process
  [[ "$EXIT_REASON" == "capture_ready_timeout" ]]
}

run_capture_matching_frame
run_capture_zero_or_wrong_frames
run_capture_malformed_json
run_capture_readiness_failure
run_capture_ready_frame_timeout
echo "ok: deepseek-harness wait_compaction"
