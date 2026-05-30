#!/usr/bin/env bash
# drive-aider.sh — run one scenario against the aider CLI headlessly.
#
# Aider stores its transcript in CWD (not under $HOME like claude/codex/pi),
# so this driver creates a fresh per-run tmp CWD and runs aider from there.
# Aider has no native session-id concept; the UUID arg is reused unchanged
# for fixture-naming parity with the other drivers.
#
# Contract files written to <staging-dir>:
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok|timeout|killed|nonzero(N)
#   transcript.path              — absolute path to <cwd>/.aider.chat.history.md
#   session.uuid                 — echoes arg $2 (aider has no session UUID)
#
# Optional env:
#   IRRLICHT_AIDER_MODEL  — passed via `aider --model` if set (e.g.
#                           openai/gemma-4-e2b-it). Otherwise aider picks
#                           up its own ~/.aider.conf.yml / env defaults.
#
# Usage:
#   drive-aider.sh <staging-dir> <session-uuid> \
#                  <timeout-seconds> <settings-path> <prompt>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-aider.sh <staging> <uuid> <timeout-s> <settings-path> <prompt>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"
TIMEOUT_S="$3"
# settings-path is accepted for ABI parity but ignored — aider uses its
# own conf file / env config.
_SETTINGS_PATH="$4"
PROMPT="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Per-run CWD under staging. Aider walks up from CWD to find a git root
# and writes .aider.chat.history.md there — without an inner git repo it
# would drop the transcript at the worktree root and pollute the tree.
# Git-init the per-run CWD so aider treats it as its own root.
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"
( cd "$RUN_CWD" \
    && git init -q \
    && git config user.email aider-onboard@local \
    && git config user.name aider-onboard \
    && : > README.md \
    && git add README.md \
    && git commit -q -m init )

AIDER_ARGS=(
  --message "$PROMPT"
  --no-auto-commits
  --yes-always
  --no-gitignore
)
if [[ -n "${IRRLICHT_AIDER_MODEL:-}" ]]; then
  AIDER_ARGS+=( --model "$IRRLICHT_AIDER_MODEL" )
fi

set +e
( cd "$RUN_CWD" && timeout --signal=SIGINT --kill-after=10 "$TIMEOUT_S" \
    aider "${AIDER_ARGS[@]}" \
    >"$DRIVER_LOG.stdout" 2>"$DRIVER_LOG.stderr" )
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

# Transcript is per-CWD. Poll briefly in case aider is still flushing
# after exit (mirrors the post-exit grace the other drivers grant).
TRANSCRIPT="$RUN_CWD/.aider.chat.history.md"
for _ in $(seq 1 60); do
  [[ -f "$TRANSCRIPT" ]] && break
  sleep 0.5
done

echo "$UUID" > "$STAGING/session.uuid"
if [[ -f "$TRANSCRIPT" ]]; then
  echo "$TRANSCRIPT" > "$STAGING/transcript.path"
else
  echo "" > "$STAGING/transcript.path"
fi

echo "drive-aider: $EXIT_REASON (uuid=$UUID, transcript=${TRANSCRIPT:-<unresolved>}, log=$DRIVER_LOG)"

exit "$EXIT_CODE"
