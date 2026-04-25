#!/usr/bin/env bash
# drive-aider-interactive.sh — drive aider's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash). For scenarios that
# can't be expressed as a single `aider --message` invocation: multi-turn
# conversations, mid-turn interrupts, /model switches.
#
# Sister script to drive-aider.sh (headless --message mode). Same staging
# contract: writes driver.log[.stdout|.stderr], driver.exit-reason,
# transcript.path, session.uuid.
#
# The 5th argument is a JSON array of steps instead of a literal prompt:
#   [{"type":"send","text":"hi"},{"type":"wait_turn"}, ...]
# Step types:
#   send       — type text, press Enter
#   slash      — same as send, but used for /commands (e.g. "/model gpt-5")
#   wait_turn  — block until a new `> Tokens: …` line appears in the
#                transcript (signals aider finished the LLM round)
#   interrupt  — Ctrl-C the current turn; the in-flight turn will NOT
#                produce a `> Tokens:` line, so don't follow it with
#                wait_turn
#   sleep      — pause N seconds (field: "seconds"); useful before an
#                interrupt to let the model start generating, or after a
#                /model switch before the next prompt
#
# Optional env:
#   IRRLICHT_AIDER_MODEL  — passed via `aider --model` if set
#
# Usage:
#   drive-aider-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-aider-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"
TIMEOUT_S="$3"
_SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Per-run git-init'd CWD — same trick as drive-aider.sh; aider walks up
# from CWD looking for a git root and writes .aider.chat.history.md there.
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"
( cd "$RUN_CWD" \
    && git init -q \
    && git config user.email aider-onboard@local \
    && git config user.name aider-onboard \
    && : > README.md \
    && git add README.md \
    && git commit -q -m init )

TRANSCRIPT="$RUN_CWD/.aider.chat.history.md"

AIDER_ARGS=( --no-auto-commits --yes-always --no-gitignore )
if [[ -n "${IRRLICHT_AIDER_MODEL:-}" ]]; then
  AIDER_ARGS+=( --model "$IRRLICHT_AIDER_MODEL" )
fi

SESSION="aider-onboard-${UUID:0:8}-$$"
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Tear down any stale session with the same name (defensive, shouldn't happen).
tmux kill-session -t "$SESSION" 2>/dev/null || true

# Start aider detached. The transcript file is the canonical record-of-truth;
# pane output goes through `tmux pipe-pane` instead of an `aider | tee`
# pipeline — the pipeline form makes Ctrl-C kill the whole process group
# including aider, which breaks the `interrupt` step type.
tmux new-session -d -s "$SESSION" -c "$RUN_CWD" \
  "aider ${AIDER_ARGS[*]}"
tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[driver] tmux started: $SESSION (model=${IRRLICHT_AIDER_MODEL:-default})" >&2

# Wait for aider to be ready for input. The transcript file is the most
# reliable readiness signal: aider writes its banner (`> Aider v…`,
# `> Model: …`, `> Repo-map: …`) to .aider.chat.history.md before the
# first input prompt, in plain Markdown — no TTY box-drawing chrome to
# confuse the grep. Cap the wait at 30s.
WAITED=0
while [[ $WAITED -lt 60 ]]; do
  if [[ -f "$TRANSCRIPT" ]] \
     && grep -qE '^> Repo-map:' "$TRANSCRIPT" 2>/dev/null; then
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done
sleep 1  # extra grace for the input prompt to settle

# Track turn completions by counting `> Tokens:` lines in the transcript.
# The transcript is the source of truth — even if the pane wraps text or
# garbles output, aider always writes a clean `> Tokens:` line at end of
# each LLM round.
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

step_send() {
  local text="$1"
  # Send literal text (no key-name expansion) then a separate Enter.
  tmux send-keys -t "$SESSION" -l -- "$text"
  tmux send-keys -t "$SESSION" Enter
  echo "[driver] send: ${text:0:60}" >&2
}

step_wait_turn() {
  local before
  before=$(turn_count)
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    local now
    now=$(turn_count)
    if [[ $now -gt $before ]]; then
      echo "[driver] wait_turn: $before → $now" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn: timeout (turn count stuck at $before)" >&2
  EXIT_REASON="timeout"
  return 1
}

step_interrupt() {
  tmux send-keys -t "$SESSION" C-c
  echo "[driver] interrupt (Ctrl-C)" >&2
  sleep 1
}

# Iterate steps. Use process substitution so EXIT_REASON updates persist
# in the parent shell.
STEP_OK=true
while read -r step; do
  if ! $STEP_OK; then break; fi
  type=$(jq -r '.type' <<<"$step")
  case "$type" in
    send|slash)
      step_send "$(jq -r '.text' <<<"$step")"
      ;;
    wait_turn)
      step_wait_turn || STEP_OK=false
      ;;
    interrupt)
      step_interrupt
      ;;
    sleep)
      secs=$(jq -r '.seconds // 1' <<<"$step")
      echo "[driver] sleep: ${secs}s" >&2
      sleep "$secs"
      ;;
    *)
      echo "[driver] unknown step type: $type" >&2
      EXIT_REASON="nonzero(2)"
      STEP_OK=false
      ;;
  esac
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Shutdown: just kill the tmux session. We deliberately do NOT send Ctrl-C
# or /exit here — both leave artifacts in the transcript:
#   - Ctrl-C → "> ^C again to exit" blockquote line
#   - /exit  → trailing "#### /exit" user_message that opens a half-turn
#             without a `> Tokens:` close, leaving the replay in `working`
# A successful script ends on `wait_turn` (or after an `interrupt`+turn-done
# pair), so there's nothing in-flight to interrupt — kill is safe.
sleep 0.5
tmux kill-session -t "$SESSION" 2>/dev/null || true

# Final pane capture for forensics.
{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
echo "$UUID" > "$STAGING/session.uuid"
if [[ -f "$TRANSCRIPT" ]]; then
  echo "$TRANSCRIPT" > "$STAGING/transcript.path"
else
  echo "" > "$STAGING/transcript.path"
fi

echo "drive-aider-interactive: $EXIT_REASON (uuid=$UUID, transcript=$TRANSCRIPT)"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
