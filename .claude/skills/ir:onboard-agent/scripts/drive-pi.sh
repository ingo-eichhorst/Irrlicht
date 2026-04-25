#!/usr/bin/env bash
# drive-pi.sh — run one scenario against the pi CLI headlessly.
#
# Pi's `--session <UUID>` resumes an existing session; we cannot
# pre-assign a UUID for a fresh session. We also can't redirect with
# `--session-dir` because the daemon's fswatcher only watches the
# default $HOME/.pi/agent/sessions tree.
#
# Strategy:
#   1. touch a marker file before launching pi
#   2. run `pi --print -p <PROMPT>` under timeout
#   3. find the new .jsonl under ~/.pi/agent/sessions newer than the
#      marker (there should be exactly one)
#   4. read its first line — pi writes the session header
#      `{"type":"session","id":"<UUID>",...}` — to extract the UUID.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — absolute path to the resolved transcript
#   session.uuid                 — actual session UUID assigned by pi
#
# Usage:
#   drive-pi.sh <staging-dir> <preferred-uuid-ignored> \
#               <timeout-seconds> <settings-path-ignored> <prompt>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-pi.sh <staging> <preferred-uuid> <timeout-s> <settings-path> <prompt>" >&2
  exit 2
fi

STAGING="$1"
PREFERRED_UUID="$2"   # pi assigns its own; we keep this arg for ABI parity
TIMEOUT_S="$3"
SETTINGS_PATH="$4"    # pi has no --settings flag; arg accepted, no-op
PROMPT="$5"

: "${PREFERRED_UUID:-}"
: "${SETTINGS_PATH:-}"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"
PI_SESSIONS_DIR="$HOME/.pi/agent/sessions"

# Marker file lets `find -newer` reliably pick up only the file pi
# creates during this run. mkdir guarantees the parent exists even if
# the user has never run pi before.
mkdir -p "$PI_SESSIONS_DIR"
MARKER="$STAGING/.pi-start-marker"
touch "$MARKER"
sleep 1   # ensure mtime resolution distinguishes new files (HFS+ = 1s)

# `pi --print -p` is the documented non-interactive mode. Auth is the
# user's responsibility (`pi --api-key` or provider env vars).
set +e
timeout --signal=SIGINT --kill-after=10 "$TIMEOUT_S" \
  pi --print -p "$PROMPT" \
  >"$DRIVER_LOG.stdout" 2>"$DRIVER_LOG.stderr"
EXIT_CODE=$?
set -e

{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout"
  echo
  echo "=== stderr ==="
  cat "$DRIVER_LOG.stderr"
  echo
  echo "=== exit code: $EXIT_CODE ==="
} >"$DRIVER_LOG"

case "$EXIT_CODE" in
  0)   EXIT_REASON="ok" ;;
  124) EXIT_REASON="timeout" ;;
  137) EXIT_REASON="killed" ;;
  *)   EXIT_REASON="nonzero($EXIT_CODE)" ;;
esac
echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Resolve the transcript: newest .jsonl under PI_SESSIONS_DIR newer than
# the marker. Poll up to 30s for pi to flush. If multiple files appear
# (e.g. another pi run interleaved), pick the lexicographically last —
# pi prefixes filenames with an ISO8601 timestamp so newest sorts last.
TRANSCRIPT=""
for _ in $(seq 1 60); do
  candidate="$(find "$PI_SESSIONS_DIR" -type f -name '*.jsonl' \
                -newer "$MARKER" 2>/dev/null | sort | tail -n1)"
  if [[ -n "$candidate" && -s "$candidate" ]]; then
    TRANSCRIPT="$candidate"
    break
  fi
  sleep 0.5
done

# Extract UUID from the session header on line 1.
UUID=""
if [[ -n "$TRANSCRIPT" ]]; then
  UUID="$(head -n1 "$TRANSCRIPT" | jq -r '.id // empty' 2>/dev/null || true)"
fi

echo "${UUID:-}" > "$STAGING/session.uuid"
echo "${TRANSCRIPT:-}" > "$STAGING/transcript.path"

echo "drive-pi: $EXIT_REASON (uuid=${UUID:-<unknown>}, transcript=${TRANSCRIPT:-<unresolved>}, log=$DRIVER_LOG)"

exit "$EXIT_CODE"
