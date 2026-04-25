#!/usr/bin/env bash
# drive-codex.sh — run one scenario against the codex CLI headlessly.
#
# codex exec has no --session-id flag; the UUID is assigned by codex and
# revealed on stdout's first event when --json is used:
#   {"type":"thread.started","thread_id":"<UUID>"}
# We capture that UUID, then locate the rollout file at
# ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<UUID>.jsonl.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — absolute path to the resolved transcript
#   session.uuid                 — actual session UUID assigned by codex
#
# Usage:
#   drive-codex.sh <staging-dir> <preferred-uuid-ignored> \
#                  <timeout-seconds> <settings-path-ignored> <prompt>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-codex.sh <staging> <preferred-uuid> <timeout-s> <settings-path> <prompt>" >&2
  exit 2
fi

STAGING="$1"
PREFERRED_UUID="$2"   # codex assigns its own; we keep this arg for ABI parity
TIMEOUT_S="$3"
SETTINGS_PATH="$4"    # codex has no --settings flag; arg accepted, no-op
PROMPT="$5"

# Reference unused args once each so `set -u` and shellcheck don't flag them.
: "${PREFERRED_UUID:-}"
: "${SETTINGS_PATH:-}"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# --json prints lifecycle events as JSONL; --skip-git-repo-check lets the
# CLI run inside worktrees and other unusual checkouts. Auth is the user's
# responsibility (`codex` config); failures surface via stderr.
set +e
timeout --signal=SIGINT --kill-after=10 "$TIMEOUT_S" \
  codex exec --json --skip-git-repo-check "$PROMPT" \
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

# Extract the assigned UUID. Don't strictly require the FIRST line — a
# future codex release could prepend a banner — but scan the first 50
# events for `thread.started`.
UUID="$(jq -r 'select(.type == "thread.started") | .thread_id' \
        < <(head -n 50 "$DRIVER_LOG.stdout") 2>/dev/null | head -n1)"

# Resolve the rollout file. Codex stores transcripts under date-stamped
# subdirs; the filename always contains the UUID, so a name-glob is
# sufficient. Poll up to 30s in case the file is still being created.
TRANSCRIPT=""
if [[ -n "$UUID" ]]; then
  for _ in $(seq 1 60); do
    candidate="$(find "$HOME/.codex/sessions" -maxdepth 4 \
                  -name "rollout-*-${UUID}.jsonl" -type f 2>/dev/null \
                | head -n1)"
    if [[ -n "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      break
    fi
    sleep 0.5
  done
fi

echo "${UUID:-}" > "$STAGING/session.uuid"
echo "${TRANSCRIPT:-}" > "$STAGING/transcript.path"

echo "drive-codex: $EXIT_REASON (uuid=${UUID:-<unknown>}, transcript=${TRANSCRIPT:-<unresolved>}, log=$DRIVER_LOG)"

exit "$EXIT_CODE"
