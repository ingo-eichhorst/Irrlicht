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

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Current-session state (mutates across `restart` steps).
CURRENT_UUID=""
CURRENT_TMUX=""
CURRENT_CWD=""
TRANSCRIPT=""
EXPECTED_TURNS=0

# Cumulative per-session records — written to staging at end so
# run-cell.sh / curate can union them into the fixture.
SESSION_UUIDS=()
SESSION_TRANSCRIPTS=()

# init_session starts a fresh `claude --session-id $CURRENT_UUID` in
# $CURRENT_CWD under tmux session $CURRENT_TMUX, accepts the trust
# dialog, waits for "auto mode on", and resolves the transcript path
# into $TRANSCRIPT. Caller must set CURRENT_UUID / CURRENT_TMUX /
# CURRENT_CWD before invoking. Resets EXPECTED_TURNS to 0 and clears
# TRANSCRIPT so the new session starts from a known state.
init_session() {
  EXPECTED_TURNS=0
  TRANSCRIPT=""
  mkdir -p "$CURRENT_CWD"
  tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true

  # Start claude REPL detached. No `claude | tee` pipeline — same
  # reason as the pi/aider drivers: a pipeline would bind interrupt
  # signals to the whole process group. Use tmux's argv-after-`--` form
  # so each flag stays its own word (no inner-string shell-quoting
  # fragility).
  tmux new-session -d -s "$CURRENT_TMUX" -c "$CURRENT_CWD" -- \
    claude --session-id "$CURRENT_UUID" --settings "$SETTINGS_PATH"
  tmux pipe-pane -t "$CURRENT_TMUX" -o "cat >> '$DRIVER_LOG.stdout'"
  echo "[driver] tmux started: $CURRENT_TMUX (uuid=$CURRENT_UUID, cwd=$CURRENT_CWD)" >&2

  # Trust dialog on first encounter with a directory. Each restart
  # uses a fresh cwd so the dialog always appears (claude caches
  # trust per-directory; reusing the cwd would skip the dialog and
  # hang the wait loop). Match against this session's expected
  # output by reading only the lines logged AFTER the tmux start —
  # otherwise an earlier session's "trust this folder" sticks around
  # in $DRIVER_LOG.stdout and our pre-trust check returns true
  # before the new claude prompts.
  local stdout_size_before=0
  if [[ -f "$DRIVER_LOG.stdout" ]]; then
    stdout_size_before=$(wc -c < "$DRIVER_LOG.stdout" | tr -d ' ')
  fi
  local WAITED=0
  while [[ $WAITED -lt 30 ]]; do
    if [[ -f "$DRIVER_LOG.stdout" ]] && \
       tail -c +$((stdout_size_before + 1)) "$DRIVER_LOG.stdout" | grep -aq 'trust this folder' 2>/dev/null; then
      tmux send-keys -t "$CURRENT_TMUX" "1"
      sleep 0.3
      tmux send-keys -t "$CURRENT_TMUX" Enter
      echo "[driver] accepted trust dialog" >&2
      break
    fi
    sleep 0.5
    WAITED=$((WAITED + 1))
  done

  # Wait for "auto mode on" — appears in the TUI footer once claude is
  # ready to accept prompts. Match the byte-window-aware way so the
  # previous session's footer doesn't false-positive.
  WAITED=0
  while [[ $WAITED -lt 60 ]]; do
    if [[ -f "$DRIVER_LOG.stdout" ]] && \
       tail -c +$((stdout_size_before + 1)) "$DRIVER_LOG.stdout" | grep -aq 'auto mode on' 2>/dev/null; then
      break
    fi
    sleep 0.5
    WAITED=$((WAITED + 1))
  done
  sleep 1  # extra grace for the input prompt to settle
}

# Resolve the transcript path. Claude writes to
# ~/.claude/projects/<slug>/<UUID>.jsonl where <slug> is derived from
# the cwd. Glob across all project dirs (cheaper than computing the
# slug) rather than walking the tree.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    for slug_dir in "$HOME"/.claude/projects/*/; do
      local candidate="$slug_dir$CURRENT_UUID.jsonl"
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

# Bring up the first session. SCRIPT_JSON's `restart` steps mint new
# UUIDs / tmux names / cwds and call init_session again.
CURRENT_UUID="$UUID"
CURRENT_TMUX="claudecode-onboard-$(date +%s)-$$"
CURRENT_CWD="$RUN_CWD"
init_session

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
step_send() {
  local text="$1"
  tmux send-keys -t "$CURRENT_TMUX" -l -- "$text"
  # Brief pause so the TUI's input handler renders the typed text
  # before Enter lands. Defensive — claudecode hasn't been seen
  # dropping Enter like codex's Ink-based input does, but the cost is
  # trivial and the symptom (text in input box, no submit) would be
  # hard to diagnose.
  sleep 0.3
  tmux send-keys -t "$CURRENT_TMUX" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: claude never created a transcript at ~/.claude/projects/*/$CURRENT_UUID.jsonl" >&2
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
  tmux send-keys -t "$CURRENT_TMUX" Escape
  if [[ $EXPECTED_TURNS -gt 0 ]]; then
    EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
  fi
  echo "[driver] interrupt (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

step_restart() {
  # End the current session's lifecycle. Record its metadata in the
  # cumulative arrays before tearing down so the epilogue can write
  # the full list to staging.
  SESSION_UUIDS+=("$CURRENT_UUID")
  SESSION_TRANSCRIPTS+=("$TRANSCRIPT")
  tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true
  sleep 1
  # Mint new identifiers for the next session. A FRESH cwd matters:
  # claude caches "trust this folder" per directory, so reusing the
  # cwd skips the dialog and the wait_for_trust loop in init_session
  # hangs forever.
  local idx=$(( ${#SESSION_UUIDS[@]} + 1 ))
  CURRENT_UUID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  CURRENT_TMUX="claudecode-onboard-$(date +%s)-$$-${idx}"
  CURRENT_CWD="${RUN_CWD}-${idx}"
  echo "[driver] restart: new session #${idx} (uuid=$CURRENT_UUID)" >&2
  init_session
}

step_sigkill() {
  # Find claude's PID by matching --session-id $CURRENT_UUID in argv.
  # Restrict to this session so concurrent claude instances aren't
  # disturbed (the user may have other claude REPLs open against the
  # same daemon in attach mode).
  local pid
  pid=$(pgrep -f "claude.*--session-id $CURRENT_UUID" | head -1)
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill: killed PID $pid (session $CURRENT_UUID)" >&2
  else
    echo "[driver] sigkill: no claude PID found for $CURRENT_UUID" >&2
  fi
  # Don't tmux kill-session — the dead-process pane stays so a later
  # restart cleans it. The kill alone produces process_exited.
  sleep 1
}

step_exit_clean() {
  # claude's TUI binds Ctrl-D to "exit". /exit isn't a recognized
  # slash command, so send Ctrl-D directly for a graceful shutdown.
  # Sleep gives claude time to write any final transcript lines
  # before its process terminates.
  tmux send-keys -t "$CURRENT_TMUX" C-d
  sleep 1
  echo "[driver] exit_clean: sent Ctrl-D to $CURRENT_TMUX" >&2
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
    restart)
      step_restart
      ;;
    sigkill)
      step_sigkill
      ;;
    exit_clean)
      step_exit_clean
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

# Final session metadata — the restart step only records sessions
# when it tears them down, so the last session needs an explicit
# entry here.
SESSION_UUIDS+=("$CURRENT_UUID")
SESSION_TRANSCRIPTS+=("$TRANSCRIPT")

sleep 0.5
tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true

{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Primary session = first one (kept for backward-compat with the
# existing single-session run-cell + curate code paths).
echo "${SESSION_UUIDS[0]}" > "$STAGING/session.uuid"
echo "${SESSION_TRANSCRIPTS[0]}" > "$STAGING/transcript.path"

# Multi-session metadata. Empty (single-session) when SESSION_UUIDS
# has only one entry — same shape, but run-cell.sh's multi-session
# branch is a no-op without `restart` steps.
printf '%s\n' "${SESSION_UUIDS[@]}" > "$STAGING/session.uuids"
printf '%s\n' "${SESSION_TRANSCRIPTS[@]}" > "$STAGING/transcript.paths"

echo "drive-claudecode-interactive: $EXIT_REASON (sessions=${#SESSION_UUIDS[@]}, primary=${SESSION_UUIDS[0]}, transcript=${SESSION_TRANSCRIPTS[0]})"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
