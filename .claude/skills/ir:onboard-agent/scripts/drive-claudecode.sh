#!/usr/bin/env bash
# drive-claudecode.sh — run one scenario against the claude CLI headlessly.
#
# Writes driver.log (stdout+stderr), driver.exit-reason, transcript.path,
# and session.uuid into the staging dir. The caller (run-cell.sh) handles
# daemon lifecycle, curation, and report generation.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — absolute path to the resolved transcript
#   session.uuid                 — actual session UUID (== arg $2 for claude)
#
# Usage:
#   drive-claudecode.sh <staging-dir> <session-uuid> \
#                       <timeout-seconds> <settings-path> <prompt>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-claudecode.sh <staging> <uuid> <timeout-s> <settings-path> <prompt>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"
TIMEOUT_S="$3"
SETTINGS_PATH="$4"
PROMPT="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# We deliberately do NOT pass --bare: it would skip the keychain and
# break subscription auth in the subprocess. The scenario's settings file
# carries any deny/allow rules; hooks fire and route to our isolated
# daemon on 7837 (which is what permission-hook-denial wants to exercise).
#
# `timeout` enforces the wall-clock cap — the agent CLI has no built-in
# hang protection. Auth is the user's responsibility (claude login /
# subscription); if unauth'd the CLI's own stderr will surface it.
set +e
timeout --signal=SIGINT --kill-after=10 "$TIMEOUT_S" \
  claude --print \
         --session-id "$UUID" \
         --settings "$SETTINGS_PATH" \
         "$PROMPT" \
  >"$DRIVER_LOG.stdout" 2>"$DRIVER_LOG.stderr"
EXIT_CODE=$?
set -e

# Combined log for easier review.
{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout"
  echo
  echo "=== stderr ==="
  cat "$DRIVER_LOG.stderr"
  echo
  echo "=== exit code: $EXIT_CODE ==="
} >"$DRIVER_LOG"

# Timeout-by-signal convention: exit 124 = timed out, 137 = killed after
# grace. Treat both as recoverable — we may still have a partial transcript.
case "$EXIT_CODE" in
  0)   EXIT_REASON="ok" ;;
  124) EXIT_REASON="timeout" ;;
  137) EXIT_REASON="killed" ;;
  *)   EXIT_REASON="nonzero($EXIT_CODE)" ;;
esac

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Resolve the transcript path. Claude Code writes transcripts to
# ~/.claude/projects/<slug>/<UUID>.jsonl. Stat the expected path under
# each slug dir (O(#projects)) rather than walking the whole tree with
# `find`. Poll up to 30s — claude may still be flushing after exit.
TRANSCRIPT=""
for _ in $(seq 1 60); do
  for slug_dir in "$HOME"/.claude/projects/*/; do
    candidate="$slug_dir$UUID.jsonl"
    if [[ -f "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      break 2
    fi
  done
  sleep 0.5
done

# Always write session.uuid; transcript.path is empty if resolution failed.
echo "$UUID" > "$STAGING/session.uuid"
echo "${TRANSCRIPT:-}" > "$STAGING/transcript.path"

echo "drive-claudecode: $EXIT_REASON (uuid=$UUID, transcript=${TRANSCRIPT:-<unresolved>}, log=$DRIVER_LOG)"

exit "$EXIT_CODE"
