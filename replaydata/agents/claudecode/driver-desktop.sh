#!/usr/bin/env bash
# Drive one no-tool Claude Code Desktop Local turn through the safe Go driver.

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: driver-desktop.sh <staging> <preferred-uuid> <timeout-s> <settings-path> <prompt>" >&2
  exit 2
fi

STAGING="$1"
TIMEOUT_S="$3"
PROMPT="$5"
DRIVER_LOG="$STAGING/driver.log"
PROMPT_FILE="$STAGING/desktop-prompt.txt"
trap 'rm -f "$PROMPT_FILE"' EXIT

: "${IRRLICHT_REPO_ROOT:?IRRLICHT_REPO_ROOT is required}"
: "${IRRLICHT_DESKTOP_DRIVER_BIN:?IRRLICHT_DESKTOP_DRIVER_BIN is required}"
: "${IRRLICHT_DESKTOP_HELPER_BIN:?IRRLICHT_DESKTOP_HELPER_BIN is required}"
: "${IRRLICHT_BIND_ADDR:?IRRLICHT_BIND_ADDR is required}"
: "${IRRLICHT_DAEMON_VERSION:?IRRLICHT_DAEMON_VERSION is required}"

printf '%s' "$PROMPT" > "$PROMPT_FILE"
mkdir -p "$STAGING/cwd"

set +e
"$IRRLICHT_DESKTOP_DRIVER_BIN" \
  --repo-root "$IRRLICHT_REPO_ROOT" \
  --staging "$STAGING" \
  --workspace "$STAGING/cwd" \
  --prompt-file "$PROMPT_FILE" \
  --helper "$IRRLICHT_DESKTOP_HELPER_BIN" \
  --daemon-address "$IRRLICHT_BIND_ADDR" \
  --recordings "$STAGING/recordings" \
  --irrlicht-version "$IRRLICHT_DAEMON_VERSION" \
  --timeout "${TIMEOUT_S}s" \
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
} > "$DRIVER_LOG"

case "$EXIT_CODE" in
  0) EXIT_REASON="ok" ;;
  *) EXIT_REASON="nonzero($EXIT_CODE)" ;;
esac
printf '%s\n' "$EXIT_REASON" > "$STAGING/driver.exit-reason"
echo "drive-claudecode-desktop: $EXIT_REASON (log=$DRIVER_LOG)"
exit "$EXIT_CODE"
