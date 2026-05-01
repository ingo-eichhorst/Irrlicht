#!/usr/bin/env bash
# drive-codex-interactive.sh — drive codex's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash). For scenarios that
# can't be expressed as a single `codex exec ...` invocation: multi-turn
# conversations, mid-turn interrupts.
#
# Sister script to drive-codex.sh (headless `codex exec` mode). Same
# staging contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid.
#
# Step types match the aider/claudecode/pi interactive drivers:
#   send       — type text, press Enter
#   slash      — same as send, used for /commands
#   wait_turn  — block until a new {"type":"event_msg","payload":{
#                "type":"task_complete",...}} line appears in the rollout
#                (signals codex finished the LLM round)
#   interrupt  — Escape the in-flight turn; codex emits
#                event_msg/turn_aborted instead of task_complete, so the
#                task_complete-only turn-counter naturally skips it
#   sleep      — pause N seconds (field: "seconds")
#
# Codex assigns its own session UUID and has no --session-id flag; both
# args are accepted for ABI parity with the other interactive drivers.
#
# Usage:
#   drive-codex-interactive.sh <staging-dir> <preferred-uuid-ignored> \
#       <timeout-seconds> <settings-path-ignored> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-codex-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# $2 (preferred-uuid) and $4 (settings-path) are accepted for ABI parity
# with the other interactive drivers; codex assigns its own UUID and has
# no --settings flag, so both are unused here.
TIMEOUT_S="$3"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"
CODEX_SESSIONS_DIR="$HOME/.codex/sessions"
mkdir -p "$CODEX_SESSIONS_DIR"

MARKER="$STAGING/.codex-start-marker"
touch "$MARKER"

# Per-run CWD so codex creates a session under a fresh path. Also keeps
# the trust dialog isolated to this run's path (codex prompts for trust
# on first encounter with any directory).
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"

SESSION="codex-onboard-$(date +%s)-$$"
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
TRANSCRIPT=""
UUID=""

tmux kill-session -t "$SESSION" 2>/dev/null || true

# Start codex REPL detached. --no-alt-screen keeps codex in inline mode,
# which makes its output capturable via tmux pipe-pane (alt-screen mode
# would clear the screen on every redraw and yield mostly noise).
tmux new-session -d -s "$SESSION" -c "$RUN_CWD" "codex --no-alt-screen"
tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout'"
echo "[driver] tmux started: $SESSION (cwd=$RUN_CWD)" >&2

# Codex shows a trust dialog on first encounter with a directory:
#   "Do you trust the contents of this directory?"
#   "1. Yes, continue / 2. No, quit"
# Each onboarding run uses a fresh CWD so the dialog always appears.
WAITED=0
while [[ $WAITED -lt 30 ]]; do
  if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'Do you trust' "$DRIVER_LOG.stdout" 2>/dev/null; then
    tmux send-keys -t "$SESSION" "1"
    sleep 0.3
    tmux send-keys -t "$SESSION" Enter
    echo "[driver] accepted trust dialog" >&2
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done

# Wait for codex's TUI banner ("OpenAI Codex (vN.N.N)") to render.
# Generous cap (90s) because codex auto-installs npm updates on launch
# when a new version is available — that step can add 30+ seconds.
WAITED=0
while [[ $WAITED -lt 180 ]]; do
  if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'OpenAI Codex' "$DRIVER_LOG.stdout" 2>/dev/null; then
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done

# After the banner renders, codex spends another ~5-15s booting its MCP
# servers (shown as "Booting MCP server: <name> (Ns • esc to interrupt)"
# in the input area). Keystrokes typed during this phase appear in the
# input box but Enter is silently swallowed. Poll the LIVE pane (not the
# history log) until "Booting MCP" is no longer rendered. Cap at 30s.
WAITED=0
while [[ $WAITED -lt 60 ]]; do
  if ! tmux capture-pane -t "$SESSION" -p -S -20 2>/dev/null | grep -q 'Booting MCP'; then
    break
  fi
  sleep 0.5
  WAITED=$((WAITED + 1))
done
sleep 2  # extra grace for the input prompt to settle

# Codex creates its rollout file under CODEX_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Defer transcript/UUID resolution until step_wait_turn (or end of
# script if there are no wait_turns).
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    local candidate
    candidate="$(find "$CODEX_SESSIONS_DIR" -maxdepth 5 -type f \
                  -name 'rollout-*.jsonl' -newer "$MARKER" 2>/dev/null \
                | sort | tail -n1)"
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      UUID="$(head -n1 "$TRANSCRIPT" | jq -r '.payload.id // empty' 2>/dev/null || true)"
      [[ -n "$UUID" ]] || { TRANSCRIPT=""; sleep 0.5; continue; }
      echo "[driver] resolve_transcript: $TRANSCRIPT (uuid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# Count completed turns by jq-counting the canonical codex turn-done
# shape: {"type":"event_msg","payload":{"type":"task_complete",...}}.
# Mirrors core/adapters/inbound/agents/codex/parser.go which classifies
# task_complete as turn_done. Other event_msg types (task_started,
# turn_aborted, agent_message, token_count) are intentionally excluded —
# in particular, an Escape-interrupted turn produces turn_aborted (not
# task_complete) and must NOT be counted as a completed turn.
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.type=="event_msg" and .payload.type=="task_complete") | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

EXPECTED_TURNS=0

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  # Brief pause so codex's Ink-based input handler renders the typed
  # text before Enter lands. Without this, Enter races the render and
  # is silently dropped — the text stays in the input box, no
  # task_started fires, no rollout file is created.
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: codex never created a rollout under $CODEX_SESSIONS_DIR" >&2
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
  # Codex's TUI binds Escape to "cancel the in-flight LLM turn" (its own
  # status footer says "esc to interrupt" while a turn is running). The
  # cancelled turn lands as event_msg/turn_aborted with no task_complete,
  # so the task_complete-only turn-counter naturally skips it.
  tmux send-keys -t "$SESSION" Escape
  if [[ $EXPECTED_TURNS -gt 0 ]]; then
    EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
  fi
  echo "[driver] interrupt (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

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

echo "drive-codex-interactive: $EXIT_REASON (uuid=$UUID, transcript=$TRANSCRIPT)"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
