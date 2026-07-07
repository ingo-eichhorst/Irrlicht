#!/usr/bin/env bash
# drive-claudecode-interactive.sh — drive claude's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash). For scenarios that
# can't be expressed as a single `claude --print -p ...` invocation:
# multi-turn conversations, mid-turn interrupts, multiple concurrent
# sessions.
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
#   keys       — send a raw tmux key sequence (Up/Down/Enter/Escape …)
#   restart    — end the active session, start a FRESH one (new uuid, new
#                cwd → re-trust)
#   resume     — end the active session, relaunch the SAME uuid+cwd via
#                `claude --resume` (daemon sees one session across PIDs)
#   reset_session — /clear: same process keeps running but rotates to a
#                new transcript/uuid
#   sigkill    — kill -9 the active session's claude PID
#   exit_clean — Ctrl-D the active session for a graceful shutdown
#
# Concurrency (multiple live sessions at once):
#   start_session — launch a NEW claude session WITHOUT tearing down the
#                   active one. Defaults to the same cwd as session 1
#                   (the multiple-sessions-same-cwd case); the already-
#                   trusted cwd means no trust dialog fires. Override the
#                   directory with {"type":"start_session","cwd":"…"}.
#   any step may carry {"session": N} to switch the active context to
#   session slot N (1-based) before executing — e.g. send a turn to
#   session 1 after start_session moved focus to session 2. A bare
#   {"type":"session","session":N} just switches focus.
#
# Session model: every session lifetime is a 1-based "slot". The initial
# session is slot 1. restart/resume/reset_session/start_session each
# allocate the next slot; restart/resume/reset_session also retire the
# previous slot (kill or rotate), start_session leaves it alive. At the
# end, ALL slots' uuids + transcripts are written to session.uuids /
# transcript.paths so run-cell.sh's multi-session curation unions them.
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

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS
# (a subset of its case arms — accepting ≠ producing), and whether slash needs a
# dedicated step type. recipe-lint reads these directly so the grammar has ONE
# owner here, not a parallel manifest. Full tmux-TUI driver; every dispatched
# step type is genuinely produced — `send|slash` is one arm, so a slash typed
# via send reaches the REPL.
DRIVE_ELICITS="send slash wait_turn interrupt keys sleep restart resume reset_session sigkill exit_clean start_session session"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

# Per-run CWD so claude writes its transcript under a unique
# ~/.claude/projects/<slug>/ dir; also keeps the trust dialog isolated to
# this run's path (claude prompts for trust on first encounter).
# run-cell-multi.sh's cross-adapter mode overrides this via
# $IRRLICHT_ONBOARD_CWD so a second, different adapter shares the SAME
# workspace (multiple-agents-same-workspace).
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Active-session view — the step functions read/write these. They are a
# cache of the active slot's state, kept in sync via save_active /
# load_slot.
CURRENT_UUID=""
CURRENT_TMUX=""
CURRENT_CWD=""
TRANSCRIPT=""
EXPECTED_TURNS=0

# Per-slot state (1-based; index 0 unused). Each slot is one session
# lifetime. SES_ALIVE[i]=1 while its tmux session is still running.
SES_UUID=()
SES_TMUX=()
SES_CWD=()
SES_TRANSCRIPT=()
SES_EXPECTED=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0

# Persist the active-view variables back into the active slot.
save_active() {
  [[ $ACTIVE -ge 1 ]] || return 0
  SES_UUID[$ACTIVE]="$CURRENT_UUID"
  SES_TMUX[$ACTIVE]="$CURRENT_TMUX"
  SES_CWD[$ACTIVE]="$CURRENT_CWD"
  SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
  SES_EXPECTED[$ACTIVE]="$EXPECTED_TURNS"
}

# Make slot $1 the active session and load its state into the view vars.
load_slot() {
  ACTIVE="$1"
  CURRENT_UUID="${SES_UUID[$ACTIVE]}"
  CURRENT_TMUX="${SES_TMUX[$ACTIVE]}"
  CURRENT_CWD="${SES_CWD[$ACTIVE]}"
  TRANSCRIPT="${SES_TRANSCRIPT[$ACTIVE]}"
  EXPECTED_TURNS="${SES_EXPECTED[$ACTIVE]}"
}

# Allocate a fresh slot (uuid, tmux, cwd) and make it active. Clears the
# view's TRANSCRIPT / EXPECTED_TURNS so the new session starts known.
alloc_slot() {
  N_SLOTS=$((N_SLOTS + 1))
  SES_UUID[$N_SLOTS]="$1"
  SES_TMUX[$N_SLOTS]="$2"
  SES_CWD[$N_SLOTS]="$3"
  SES_TRANSCRIPT[$N_SLOTS]=""
  SES_EXPECTED[$N_SLOTS]=0
  SES_ALIVE[$N_SLOTS]=1
  ACTIVE=$N_SLOTS
  CURRENT_UUID="$1"
  CURRENT_TMUX="$2"
  CURRENT_CWD="$3"
  TRANSCRIPT=""
  EXPECTED_TURNS=0
}

# Has CURRENT_CWD already been used (and therefore trusted) by an earlier
# slot in THIS run? If so, the second claude in that dir won't get a
# "trust this folder" prompt and the trust wait must be skipped.
cwd_already_trusted() {
  local i
  for (( i = 1; i < ACTIVE; i++ )); do
    [[ "${SES_CWD[$i]}" == "$CURRENT_CWD" ]] && return 0
  done
  return 1
}

# init_session starts a fresh `claude --session-id $CURRENT_UUID` in
# $CURRENT_CWD under tmux session $CURRENT_TMUX, accepts the trust
# dialog (unless the cwd is already trusted by an earlier session this
# run), waits for "auto mode on", and resolves the transcript path into
# $TRANSCRIPT. Caller sets the active slot (alloc_slot) before invoking.
#
# Resume mode: when RESUME_MODE=1, replaces --session-id with
# --resume <UUID> so claude appends to the existing transcript at
# ~/.claude/projects/<slug>/<UUID>.jsonl. Passing --session-id twice
# with the same UUID would conflict — claude exits immediately.
init_session() {
  mkdir -p "$CURRENT_CWD"
  tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true

  # Per-slot stdout capture. With concurrent sessions, a single shared
  # stdout file would interleave both panes' TUI refreshes and confuse
  # the trust / auto-mode detection. Each slot pipes to its own file;
  # the epilogue concatenates them into driver.log.
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"

  # Start claude REPL detached. No `claude | tee` pipeline — same
  # reason as the pi/aider drivers: a pipeline would bind interrupt
  # signals to the whole process group. Use tmux's argv-after-`--` form
  # so each flag stays its own word (no inner-string shell-quoting
  # fragility).
  local -a claude_args=(--settings "$SETTINGS_PATH")
  if [[ "${RESUME_MODE:-0}" == "1" ]]; then
    claude_args+=(--resume "$CURRENT_UUID")
  else
    claude_args+=(--session-id "$CURRENT_UUID")
  fi
  tmux new-session -d -s "$CURRENT_TMUX" -c "$CURRENT_CWD" -- \
    claude "${claude_args[@]}"
  tmux pipe-pane -t "$CURRENT_TMUX" -o "cat >> '$slot_stdout'"
  echo "[driver] tmux started: $CURRENT_TMUX (slot=$ACTIVE, uuid=$CURRENT_UUID, cwd=$CURRENT_CWD, mode=${RESUME_MODE:+resume})" >&2

  # Startup gate dialog(s) on first encounter with a directory. Two kinds
  # can fire, both answered "1 → Enter" (the allow/trust option):
  #   1. "trust this folder" — the per-directory trust prompt.
  #   2. "Allow external CLAUDE.md file imports?" — fires when the cwd is
  #      nested under a project whose CLAUDE.md @-imports files OUTSIDE the
  #      cwd (e.g. the run-cell-multi shared cwd lives under the irrlicht
  #      worktree, whose CLAUDE.md imports ../AGENTS.md). This dialog
  #      appears BEFORE the trust prompt and, if left unanswered, blocks
  #      the REPL forever — claude reaches "auto mode on" but never starts
  #      a turn, so no transcript is created → readiness_timeout.
  # claude caches both decisions per-directory, so a concurrent session
  # sharing an already-trusted cwd never sees either prompt — skip the
  # wait so we don't stall for dialogs that will never appear. We keep
  # scanning until "auto mode on" so BOTH dialogs (import-then-trust) get
  # answered if they fire in sequence.
  if cwd_already_trusted; then
    echo "[driver] trust: cwd already trusted by an earlier session — skipping prompt" >&2
  else
    local waited=0 import_done=0 trust_done=0
    while [[ $waited -lt 30 ]]; do
      if [[ -f "$slot_stdout" ]]; then
        # The TUI lays the dialog title out with per-word cursor-move
        # escapes (e.g. "external\e[18GCLAUDE.md\e[28Gimports"), so a
        # literal multi-word phrase never matches the raw pane bytes.
        # The dialog is uniquely identified by both "external" AND
        # "imports" being present (the trust-folder prompt has neither).
        if [[ $import_done -eq 0 ]] && \
           grep -aq 'external' "$slot_stdout" 2>/dev/null && \
           grep -aq 'imports' "$slot_stdout" 2>/dev/null; then
          tmux send-keys -t "$CURRENT_TMUX" "1"
          sleep 0.3
          tmux send-keys -t "$CURRENT_TMUX" Enter
          echo "[driver] accepted external-CLAUDE.md-imports dialog" >&2
          import_done=1
          sleep 0.5
          continue
        fi
        if [[ $trust_done -eq 0 ]] && \
           grep -aq 'trust this folder' "$slot_stdout" 2>/dev/null; then
          tmux send-keys -t "$CURRENT_TMUX" "1"
          sleep 0.3
          tmux send-keys -t "$CURRENT_TMUX" Enter
          echo "[driver] accepted trust dialog" >&2
          trust_done=1
        fi
        # Both kinds handled (or whichever fired) once the footer is up.
        grep -aq 'auto mode on' "$slot_stdout" 2>/dev/null && break
      fi
      sleep 0.5
      waited=$((waited + 1))
    done
  fi

  # Wait for "auto mode on" — appears in the TUI footer once claude is
  # ready to accept prompts.
  local waited=0
  while [[ $waited -lt 60 ]]; do
    if [[ -f "$slot_stdout" ]] && \
       grep -aq 'auto mode on' "$slot_stdout" 2>/dev/null; then
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
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
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn: claude never created a transcript at ~/.claude/projects/*/$CURRENT_UUID.jsonl" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  # The Workflow tool fires an in-turn "Run a dynamic workflow?" permission
  # dialog mid-turn (after claude emits the Workflow tool_use). Auto mode
  # doesn't cover it, so an unanswered dialog stalls the REPL until timeout.
  # Option 1 ("Yes, run it") is pre-selected, so a bare Enter accepts it.
  # Poll the active slot's pane capture and accept once.
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  local workflow_dialog_done=0
  local now=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    if [[ $workflow_dialog_done -eq 0 ]] && [[ -f "$slot_stdout" ]] && \
       grep -aq 'Run a dynamic workflow?' "$slot_stdout" 2>/dev/null; then
      tmux send-keys -t "$CURRENT_TMUX" Enter
      echo "[driver] accepted dynamic-workflow permission dialog" >&2
      workflow_dialog_done=1
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected ≥ $EXPECTED_TURNS)" >&2
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
  echo "[driver] interrupt[s$ACTIVE] (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

# step_keys sends a raw tmux key sequence (NOT literal text). Useful for
# driving picker UIs like /rewind's checkpoint selector — Escape, Up,
# Down, Enter, etc. Each space-separated token becomes one tmux key
# event. Unlike step_send, no implicit Enter is appended.
#
# Recipe step shape:
#   {"type": "keys", "keys": "Escape Escape"}
#   {"type": "keys", "keys": "Up Up Enter"}
step_keys() {
  local keys="$1"
  # shellcheck disable=SC2086 — intentional word-splitting of the key list
  tmux send-keys -t "$CURRENT_TMUX" $keys
  echo "[driver] keys[s$ACTIVE]: $keys" >&2
  sleep 0.3
}

step_restart() {
  # End the active session's lifecycle and start a FRESH one. The old
  # slot is preserved (save_active) so the epilogue still flushes its
  # uuid + transcript; we just retire it (kill its tmux) and allocate a
  # new slot for the next session.
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true
  sleep 1
  # Mint new identifiers. A FRESH cwd matters: claude caches "trust this
  # folder" per directory, so reusing the cwd skips the dialog and the
  # wait_for_trust loop in init_session hangs forever.
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot \
    "$(uuidgen | tr '[:upper:]' '[:lower:]')" \
    "claudecode-onboard-$(date +%s)-$$-${idx}" \
    "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (uuid=$CURRENT_UUID)" >&2
  init_session
}

step_resume() {
  # Resume the active session — same UUID + same cwd as the previous
  # launch. init_session uses `claude --resume <UUID>` (not --session-id)
  # so claude loads the existing transcript and the daemon observes the
  # SAME UUID-keyed session row across the new PID lifetime. Passing
  # --session-id twice would conflict — claude exits immediately.
  save_active
  SES_ALIVE[$ACTIVE]=0
  local prev_uuid="$CURRENT_UUID" prev_cwd="$CURRENT_CWD"
  tmux kill-session -t "$CURRENT_TMUX" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  # SAME uuid + SAME cwd — that's the whole point of resume.
  alloc_slot "$prev_uuid" "claudecode-onboard-$(date +%s)-$$-${idx}" "$prev_cwd"
  echo "[driver] resume: same uuid=$CURRENT_UUID, same cwd=$CURRENT_CWD, new tmux=$CURRENT_TMUX" >&2
  RESUME_MODE=1 init_session
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
  SES_ALIVE[$ACTIVE]=0
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
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean: sent Ctrl-D to $CURRENT_TMUX" >&2
}

step_reset_session() {
  # /clear in claudecode abandons the current conversation and starts a
  # fresh one — claude writes future turns to a NEW transcript file (new
  # UUID) under the same project slug, in the SAME process. The driver
  # records the old slot, then allocates a new slot reusing the same
  # tmux/process/cwd but pointing at the new uuid/transcript.
  local old_transcript="$TRANSCRIPT"
  local old_uuid="$CURRENT_UUID"
  local old_tmux="$CURRENT_TMUX"
  local old_cwd="$CURRENT_CWD"
  tmux send-keys -t "$CURRENT_TMUX" "/clear"
  sleep 0.3
  tmux send-keys -t "$CURRENT_TMUX" Enter
  echo "[driver] reset_session: sent /clear (old uuid=$old_uuid)" >&2
  # Retire the old slot (its uuid is no longer being written) but keep
  # it in the slot list so the epilogue flushes it.
  save_active
  SES_ALIVE[$ACTIVE]=0
  sleep 2

  # Find the new transcript: same slug dir, different UUID, non-empty.
  local slug_dir=""
  if [[ -n "$old_transcript" ]]; then
    slug_dir="$(dirname "$old_transcript")"
  fi
  local new_transcript=""
  if [[ -n "$slug_dir" && -d "$slug_dir" ]]; then
    for _ in $(seq 1 30); do
      for candidate in "$slug_dir"/*.jsonl; do
        [[ -f "$candidate" ]] || continue
        [[ "$candidate" == "$old_transcript" ]] && continue
        if [[ -s "$candidate" ]]; then
          new_transcript="$candidate"
          break 2
        fi
      done
      sleep 0.5
    done
  fi

  # New slot reuses the SAME tmux/process/cwd (claude didn't relaunch).
  local new_uuid="$old_uuid"
  if [[ -n "$new_transcript" ]]; then
    new_uuid="$(basename "$new_transcript" .jsonl)"
  else
    echo "[driver] reset_session: WARNING — no new transcript detected after /clear; subsequent turns may target the OLD UUID" >&2
  fi
  alloc_slot "$new_uuid" "$old_tmux" "$old_cwd"
  TRANSCRIPT="$new_transcript"
  SES_TRANSCRIPT[$ACTIVE]="$new_transcript"
  if [[ -n "$new_transcript" ]]; then
    echo "[driver] reset_session: new uuid=$CURRENT_UUID at $new_transcript" >&2
  fi
}

step_start_session() {
  # Launch a NEW concurrent claude session WITHOUT tearing down the
  # active one. The previous session keeps running (its tmux survives),
  # so the daemon observes both as independent session rows. Defaults to
  # session 1's cwd (the same-cwd scenario); pass a directory to launch
  # elsewhere.
  local req_cwd="$1"
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot \
    "$(uuidgen | tr '[:upper:]' '[:lower:]')" \
    "claudecode-onboard-$(date +%s)-$$-${idx}" \
    "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (uuid=$CURRENT_UUID, cwd=$new_cwd)" >&2
  init_session
}

# Bring up the first session as slot 1. SCRIPT_JSON's restart/resume/
# reset_session/start_session steps allocate further slots.
alloc_slot "$UUID" "claudecode-onboard-$(date +%s)-$$" "$RUN_CWD"
init_session

# Iterate steps. EXIT_REASON / array updates persist via the parent shell
# (process substitution feeds the loop — the body is NOT subshelled).
STEP_OK=true
while read -r step; do
  if ! $STEP_OK; then break; fi
  type=$(jq -r '.type' <<<"$step")

  # Optional inline session target: switch the active context to slot N
  # before executing the step. start_session is exempt (it allocates its
  # own slot). A target slot must already exist.
  tgt=$(jq -r '.session // empty' <<<"$step")
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (uuid=$CURRENT_UUID)" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"
      STEP_OK=false
      continue
    fi
  fi

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
    keys)
      step_keys "$(jq -r '.keys' <<<"$step")"
      ;;
    sleep)
      secs=$(jq -r '.seconds // 1' <<<"$step")
      echo "[driver] sleep: ${secs}s" >&2
      sleep "$secs"
      ;;
    restart)
      step_restart
      ;;
    resume)
      step_resume
      ;;
    reset_session)
      step_reset_session
      ;;
    sigkill)
      step_sigkill
      ;;
    exit_clean)
      step_exit_clean
      ;;
    start_session)
      step_start_session "$(jq -r '.cwd // empty' <<<"$step")"
      ;;
    session)
      # Pure focus switch — already handled by the inline target block.
      :
      ;;
    *)
      echo "[driver] unknown step type: $type" >&2
      EXIT_REASON="nonzero(2)"
      STEP_OK=false
      ;;
  esac
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Persist the final active state.
save_active

# Best-effort: any slot that never resolved a transcript (e.g. a script
# with no wait_turn for that session) gets one last resolution attempt.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -z "${SES_TRANSCRIPT[$i]}" ]]; then
    load_slot "$i"
    resolve_transcript || true
    save_active
  fi
done

sleep 0.5

# Tear down every still-alive session.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ "${SES_ALIVE[$i]}" == "1" ]]; then
    tmux kill-session -t "${SES_TMUX[$i]}" 2>/dev/null || true
  fi
done

{
  echo "=== stdout ==="
  for (( i = 1; i <= N_SLOTS; i++ )); do
    if [[ -f "$DRIVER_LOG.stdout.$i" ]]; then
      echo "--- session slot $i (uuid=${SES_UUID[$i]}) ---"
      cat "$DRIVER_LOG.stdout.$i" 2>/dev/null || true
      echo
    fi
  done
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"
# Keep a combined .stdout for backward-compat with any tooling that reads it.
cat "$DRIVER_LOG".stdout.* > "$DRIVER_LOG.stdout" 2>/dev/null || true

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Primary session = slot 1 (kept for backward-compat with the existing
# single-session run-cell + curate code paths).
echo "${SES_UUID[1]}" > "$STAGING/session.uuid"
echo "${SES_TRANSCRIPT[1]}" > "$STAGING/transcript.path"

# Multi-session metadata. Single-session runs write one line each — same
# shape, run-cell.sh's multi-session branch is a no-op below count 2.
: > "$STAGING/session.uuids"
: > "$STAGING/transcript.paths"
for (( i = 1; i <= N_SLOTS; i++ )); do
  echo "${SES_UUID[$i]}" >> "$STAGING/session.uuids"
  echo "${SES_TRANSCRIPT[$i]}" >> "$STAGING/transcript.paths"
done

echo "drive-claudecode-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=${SES_UUID[1]}, transcript=${SES_TRANSCRIPT[1]})"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
