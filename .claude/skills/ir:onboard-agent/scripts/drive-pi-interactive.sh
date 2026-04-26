#!/usr/bin/env bash
# drive-pi-interactive.sh — drive pi's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash). For scenarios that
# can't be expressed as a single `pi --print -p ...` invocation:
# multi-turn conversations, mid-turn interrupts.
#
# Sister script to drive-pi.sh (headless --print mode). Same staging
# contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid.
#
# Step types match the aider interactive driver (single source of truth
# for the script schema):
#   send       — type text, press Enter
#   slash      — same as send, used for /commands
#   wait_turn  — block until a new {"type":"message","message":{"role":
#                "assistant","stopReason":"stop",...}} line appears in
#                the transcript (signals pi finished the LLM round)
#   interrupt  — Ctrl-C the in-flight turn; the cancelled turn will NOT
#                produce a stopReason="stop" line, so don't follow it
#                with wait_turn
#   sleep      — pause N seconds (field: "seconds")
#
# Pi assigns its own session UUID and has no --settings flag; both args
# are accepted for ABI parity with the other interactive drivers.
#
# Usage:
#   drive-pi-interactive.sh <staging-dir> <preferred-uuid-ignored> \
#       <timeout-seconds> <settings-path-ignored> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-pi-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# $2 (preferred-uuid) and $4 (settings-path) are accepted for ABI parity
# with the other interactive drivers; pi assigns its own UUID and has no
# --settings flag, so both are unused here.
TIMEOUT_S="$3"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"
PI_SESSIONS_DIR="$HOME/.pi/agent/sessions"
mkdir -p "$PI_SESSIONS_DIR"

# Marker scopes the post-launch `find -newer` lookup to this invocation,
# matching the pattern in drive-pi.sh.
MARKER="$STAGING/.pi-start-marker"
touch "$MARKER"

# Per-run CWD so pi creates a session under a unique project dir
# (~/.pi/agent/sessions/<projectdir>/<ts>_<uuid>.jsonl). The dir lives
# inside the staging tree, and the slugified projectdir keeps reruns
# from colliding.
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"

SESSION="pi-onboard-$(date +%s)-$$"
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
TRANSCRIPT=""
UUID=""

# Tear down any stale tmux session with the same name (defensive).
tmux kill-session -t "$SESSION" 2>/dev/null || true

# Start pi detached. No `pi | tee` pipeline — same reason as the aider
# driver: a pipeline binds Ctrl-C to the whole process group and would
# kill pi instead of just the in-flight turn.
tmux new-session -d -s "$SESSION" -c "$RUN_CWD" "pi"
tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[driver] tmux started: $SESSION (cwd=$RUN_CWD)" >&2

# Wait for pi REPL to print its "Ready" line (lower-right of the TUI
# chrome). The pane log is full of ANSI escapes, but the literal
# substring is uncorrupted — grep -a treats it as text. Cap at 30s.
WAITED=0
while [[ $WAITED -lt 60 ]]; do
  if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'Ready' "$DRIVER_LOG.stdout" 2>/dev/null; then
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done
sleep 1  # extra grace for the input prompt to settle

# Pi creates its transcript file under PI_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Defer transcript/UUID resolution until step_wait_turn (or end of
# script if there are no wait_turns).
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    local candidate
    candidate="$(find "$PI_SESSIONS_DIR" -type f -name '*.jsonl' \
                  -newer "$MARKER" 2>/dev/null | sort | tail -n1)"
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      UUID="$(head -n1 "$TRANSCRIPT" | jq -r '.id // empty' 2>/dev/null || true)"
      [[ -n "$UUID" ]] || { TRANSCRIPT=""; sleep 0.5; continue; }
      echo "[driver] resolve_transcript: $TRANSCRIPT (uuid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# Count completed assistant turns by jq-counting the canonical pi
# turn-done shape: {"type":"message","message":{"role":"assistant",
# "stopReason":"stop",...}}. This mirrors core/adapters/inbound/agents/
# pi/parser.go which classifies stopReason=="stop" as turn_done. A grep
# would also work since both fields appear on the same line, but jq
# avoids false positives from assistant-text content that happens to
# contain those substrings.
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.type=="message" and .message.role=="assistant" and .message.stopReason=="stop") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

# Track expected vs. actual completed turns. Each `send` bumps EXPECTED
# by 1; wait_turn waits for actual >= expected. Without this, wait_turn
# races against pi finishing the turn before resolve_transcript returns
# — pi finishes a one-word reply in <2s, faster than the find-poll for
# a freshly-created transcript file, so a naive
#   before=turn_count(); wait until now>before
# snapshots before=1 and waits forever.
EXPECTED_TURNS=0

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: pi never created a transcript under $PI_SESSIONS_DIR" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    local now
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn: timeout (count=$(turn_count), expected ≥ $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

step_interrupt() {
  # Pi binds Escape to "interrupt the in-flight LLM turn" (per its
  # startup banner: "escape interrupt · ctrl+c/ctrl+d clear/exit").
  # Ctrl-C in pi clears the input buffer or exits on second press, so
  # using it here would either be a no-op (when idle) or kill the REPL.
  tmux send-keys -t "$SESSION" Escape
  # The cancelled turn won't produce a stop message, so the EXPECTED_TURNS
  # bump from the preceding send is "consumed" with no actual increment.
  # Decrement so the next send + wait_turn pair stays in sync.
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
# without any wait_turn step), try once more before tearing down so
# session.uuid + transcript.path are populated for run-cell.sh.
if [[ -z "$TRANSCRIPT" ]]; then
  resolve_transcript || true
fi

# Shutdown: kill the tmux session. Mirrors the aider driver — successful
# scripts end on wait_turn (or interrupt+turn-done) so there's nothing
# in-flight to interrupt; sending /exit or Ctrl-C here would just leave
# extra noise in the transcript and the captured pane log.
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

echo "drive-pi-interactive: $EXIT_REASON (uuid=$UUID, transcript=$TRANSCRIPT)"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
