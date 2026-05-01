#!/usr/bin/env bash
# drive-claudecode-interactive.sh — drive claude's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash). For scenarios that
# can't be expressed as a single `claude --print -p ...` invocation:
# multi-turn conversations, mid-turn interrupts.
#
# Sister script to drive-claudecode.sh (headless --print mode). Same staging
# contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid.
#
# Step types match the aider/pi interactive drivers:
#   send       — type text, press Enter
#   slash      — same as send, used for /commands
#   wait_turn  — block until a new {"type":"assistant","message":{...,
#                "stop_reason":"end_turn",...}} line appears in the
#                transcript (signals claude finished the LLM round)
#   interrupt  — Escape the in-flight turn; the cancelled turn produces a
#                non-end_turn assistant message (typically
#                stop_reason="stop_sequence"), so the end_turn-only
#                turn-counter naturally skips it
#   sleep      — pause N seconds (field: "seconds")
#
# claude assigns the session UUID via --session-id (driver passes the
# preferred UUID through, just like drive-claudecode.sh).
#
# Usage:
#   drive-claudecode-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-claudecode-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"
TIMEOUT_S="$3"
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Per-run CWD so claude writes its transcript under a unique
# ~/.claude/projects/<slug>/ dir; also keeps the trust dialog isolated to
# this run's path (claude prompts for trust on first encounter).
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"

SESSION="claudecode-onboard-$(date +%s)-$$"
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
TRANSCRIPT=""

tmux kill-session -t "$SESSION" 2>/dev/null || true

# Start claude REPL detached. No `claude | tee` pipeline — same reason as
# the pi/aider drivers: a pipeline would bind interrupt signals to the
# whole process group. Use tmux's argv-after-`--` form so each flag
# stays its own word (no inner-string shell-quoting fragility).
tmux new-session -d -s "$SESSION" -c "$RUN_CWD" -- \
  claude --session-id "$UUID" --settings "$SETTINGS_PATH"
tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[driver] tmux started: $SESSION (uuid=$UUID, cwd=$RUN_CWD)" >&2

# Claude shows a trust dialog on first encounter with a directory:
#   "Is this a project you created or one you trust?"
#   "1. Yes, I trust this folder / 2. No, exit"
# Each onboarding run uses a fresh CWD so the dialog always appears.
# Wait for it then send "1" + Enter to accept; afterwards the TUI
# initializes fully.
WAITED=0
while [[ $WAITED -lt 30 ]]; do
  if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'trust this folder' "$DRIVER_LOG.stdout" 2>/dev/null; then
    tmux send-keys -t "$SESSION" "1"
    sleep 0.3
    tmux send-keys -t "$SESSION" Enter
    echo "[driver] accepted trust dialog" >&2
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done

# Wait for the TUI footer to print "auto mode on" — its presence means the
# input area has rendered and claude is ready to accept prompts. The pane
# log is full of ANSI escapes, but the literal substring is uncorrupted —
# grep -a treats it as text. Cap at 30s.
WAITED=0
while [[ $WAITED -lt 60 ]]; do
  if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'auto mode on' "$DRIVER_LOG.stdout" 2>/dev/null; then
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done
sleep 1  # extra grace for the input prompt to settle

# Resolve the transcript path. Claude writes to
# ~/.claude/projects/<slug>/<UUID>.jsonl where <slug> is derived from
# RUN_CWD. Glob across all project dirs (cheaper than computing the slug)
# rather than walking the tree.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    for slug_dir in "$HOME"/.claude/projects/*/; do
      local candidate="$slug_dir$UUID.jsonl"
      if [[ -f "$candidate" && -s "$candidate" ]]; then
        TRANSCRIPT="$candidate"
        echo "[driver] resolve_transcript: $TRANSCRIPT" >&2
        return 0
      fi
    done
    sleep 0.5
  done
  return 1
}

# Count completed assistant turns by jq-counting the canonical claude
# turn-done shape: top-level type=="assistant" with
# .message.stop_reason=="end_turn". Mirrors core/adapters/inbound/agents/
# claudecode/parser.go which classifies end_turn as turn_done. Other
# stop_reasons (tool_use, max_tokens, stop_sequence) signify either
# mid-turn pauses or aborts and are intentionally excluded — in
# particular, an Escape-interrupted message lands as stop_sequence and
# must NOT be counted as a completed turn.
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.type=="assistant" and .message.stop_reason=="end_turn") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

# Track expected vs. actual completed turns. Each `send` bumps EXPECTED
# by 1; wait_turn waits for actual >= expected. Without this, wait_turn
# can race against claude finishing the turn before resolve_transcript
# returns — see the pi driver for the original analysis.
EXPECTED_TURNS=0

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  # Brief pause so the TUI's input handler renders the typed text before
  # Enter lands. Defensive — claudecode hasn't been seen dropping Enter
  # like codex's Ink-based input does, but the cost is trivial and the
  # symptom (text in input box, no submit) would be hard to diagnose.
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: claude never created a transcript at ~/.claude/projects/*/$UUID.jsonl" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn: timeout (count=$now, expected ≥ $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

step_interrupt() {
  # Claude's TUI binds Escape to "cancel the in-flight LLM turn" — the
  # interrupted message lands with stop_reason="stop_sequence" (not
  # end_turn), so the end_turn-only turn-counter naturally skips it.
  tmux send-keys -t "$SESSION" Escape
  if [[ $EXPECTED_TURNS -gt 0 ]]; then
    EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
  fi
  echo "[driver] interrupt (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

# Iterate steps. EXIT_REASON updates persist via the parent shell
# (process substitution feeds the loop).
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

# If we never resolved the transcript via wait_turn (e.g. a script
# without any wait_turn step), try once more before tearing down.
if [[ -z "$TRANSCRIPT" ]]; then
  resolve_transcript || true
fi

sleep 0.5
tmux kill-session -t "$SESSION" 2>/dev/null || true

{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
echo "$UUID" > "$STAGING/session.uuid"
echo "$TRANSCRIPT" > "$STAGING/transcript.path"

echo "drive-claudecode-interactive: $EXIT_REASON (uuid=$UUID, transcript=$TRANSCRIPT)"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
