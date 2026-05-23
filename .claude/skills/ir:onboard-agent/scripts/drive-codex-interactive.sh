#!/usr/bin/env bash
# drive-codex-interactive.sh — drive codex's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash / …). For scenarios
# that can't be expressed as a single `codex exec ...` invocation:
# multi-turn conversations, mid-turn interrupts, /clear and /fork
# session swaps, and resume relaunches.
#
# Sister script to drive-codex.sh (headless `codex exec` mode). Same
# staging contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid — PLUS the multi-session lists
# session.uuids / transcript.paths (newline-separated, in order) so
# run-cell.sh / curate union ALL sessions a single run produces into
# the fixture. Mirrors drive-claudecode-interactive.sh's multi-session
# contract, adapted to codex's rollout-discovery model.
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
#   reset_session — send /clear; codex abandons the current conversation
#                and writes the NEXT prompt's turns to a brand-new rollout
#                (new session_id). The driver records the old session and
#                re-discovers the new rollout on the next wait_turn.
#   fork       — send /fork; codex clones the conversation into a new
#                thread with a fresh session_id. Same new-rollout
#                discovery as reset_session.
#   exit_clean — Ctrl-D for a graceful shutdown (codex flushes its
#                rollout and the daemon emits process_exited).
#   resume     — Ctrl-D the current codex, kill the tmux session, then
#                relaunch `codex resume <UUID> --no-alt-screen`. Codex
#                APPENDS to the SAME rollout (same session_id) across the
#                two process lifetimes (verified empirically), so the
#                session identity is kept unchanged — no second array
#                entry is appended for the resumed half.
#
# Codex assigns its OWN session UUID per rollout and has no --session-id
# flag; both args are accepted for ABI parity with the other interactive
# drivers.
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

# MARKER gates rollout discovery: resolve_transcript only considers
# rollout files NEWER than the marker. reset_session / fork bump it so
# the NEXT discovery skips the prior (now-frozen) rollout and finds the
# fresh one. resume does NOT bump it — the resumed process appends to
# the same (already-discovered) rollout.
MARKER="$STAGING/.codex-start-marker"
touch "$MARKER"

# Per-run CWD so codex creates a session under a fresh path. Also keeps
# the trust dialog isolated to this run's path (codex prompts for trust
# on first encounter with any directory).
RUN_CWD="$STAGING/cwd"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Current-session state. TRANSCRIPT/UUID are the codex-side identifiers
# for the live rollout: UUID is `.payload.id` (the bare conversation
# UUID, used as the `codex resume <UUID>` argument); TRANSCRIPT is the
# absolute rollout-*.jsonl path. SESSION is the tmux session name; it
# rotates across resume relaunches.
SESSION="codex-onboard-$(date +%s)-$$"
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0

# Cumulative per-session records — written to staging at end so
# run-cell.sh / curate can union them into the fixture. Each entry is
# the DAEMON-side session_id (the rollout filename minus ".jsonl", i.e.
# `rollout-<ts>-<uuid>`) — NOT the bare `.payload.id`. The daemon keys
# sessions on that filename stem (see fswatcher extractSessionID), and
# curate-lifecycle-fixture.sh filters events by `.session_id`, so the
# array MUST hold the filename-stem form for the fixture filter to match.
SESSION_IDS=()
SESSION_TRANSCRIPTS=()

# daemon_sid maps an absolute rollout path to the daemon's session_id
# (basename minus ".jsonl").
daemon_sid() {
  local p="$1"
  [[ -z "$p" ]] && { echo ""; return; }
  local b; b="$(basename "$p")"
  echo "${b%.jsonl}"
}

tmux kill-session -t "$SESSION" 2>/dev/null || true

# boot_session brings up a codex TUI in $SESSION running the given argv,
# accepts the trust dialog, waits for the "OpenAI Codex" banner, and
# waits out the "Booting MCP" phase. Caller sets $SESSION first.
#
# Launch/boot notes:
#   --no-alt-screen keeps codex in inline mode so its output is
#   capturable via tmux pipe-pane (alt-screen would clear the screen on
#   every redraw and yield mostly noise).
#
#   Trust dialog: codex shows "Do you trust the contents of this
#   directory?" on first encounter with a directory. The pipe-pane LOG
#   splits that string across cursor-positioning escapes, so a literal
#   grep on the LOG misses it — poll the LIVE pane via capture-pane
#   instead, which renders the text contiguously. Each run uses a fresh
#   cwd so the dialog always appears.
#
#   Banner: "OpenAI Codex (vN.N.N)" renders contiguously in the LOG.
#   Generous cap (90s) because codex may auto-install npm updates on
#   launch.
#
#   Booting MCP: codex then spends ~5-15s booting MCP servers; keystrokes
#   typed during this phase have their Enter silently swallowed. Poll the
#   LIVE pane until "Booting MCP" is gone.
boot_session() {
  local sess="$1"; shift
  tmux new-session -d -s "$sess" -c "$RUN_CWD" "$@"
  tmux pipe-pane -t "$sess" -o "cat >> '$DRIVER_LOG.stdout'"
  echo "[driver] tmux started: $sess (cwd=$RUN_CWD, argv: $*)" >&2

  local WAITED=0
  while [[ $WAITED -lt 30 ]]; do
    if tmux capture-pane -t "$sess" -p -S -40 2>/dev/null | grep -q 'Do you trust'; then
      tmux send-keys -t "$sess" "1"
      sleep 0.3
      tmux send-keys -t "$sess" Enter
      echo "[driver] accepted trust dialog" >&2
      break
    fi
    sleep 0.5
    WAITED=$((WAITED + 1))
  done

  WAITED=0
  while [[ $WAITED -lt 180 ]]; do
    if [[ -f "$DRIVER_LOG.stdout" ]] && grep -aq 'OpenAI Codex' "$DRIVER_LOG.stdout" 2>/dev/null; then
      break
    fi
    sleep 0.5
    WAITED=$((WAITED + 1))
  done

  WAITED=0
  while [[ $WAITED -lt 60 ]]; do
    if ! tmux capture-pane -t "$sess" -p -S -20 2>/dev/null | grep -q 'Booting MCP'; then
      break
    fi
    sleep 0.5
    WAITED=$((WAITED + 1))
  done
  sleep 2  # extra grace for the input prompt to settle
}

# Bring up the first session.
boot_session "$SESSION" codex --no-alt-screen

# Codex creates its rollout file under CODEX_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Defer transcript/UUID resolution until step_wait_turn (or end of
# script if there are no wait_turns). Discovery finds the newest
# rollout-*.jsonl NEWER than $MARKER; after a /clear or /fork (which bump
# $MARKER) the prior rollout is excluded so the new one is picked up.
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
      echo "[driver] resolve_transcript: $TRANSCRIPT (uuid=$UUID, sid=$(daemon_sid "$TRANSCRIPT"))" >&2
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

# swap_after_slash <slash-text> — shared handler for /clear (reset_session)
# and /fork (fork). Both abandon the current rollout and cause codex to
# write subsequent turns to a NEW rollout with a fresh session_id:
#   /clear is LAZY  — the new rollout materializes only on the first
#                     post-clear user message.
#   /fork  is EAGER — the new rollout materializes the instant the
#                     command runs (carrying replayed pre-fork history).
# Either way the discovery is identical: record the current session,
# send the slash, bump $MARKER, and clear the cached TRANSCRIPT/UUID +
# reset EXPECTED_TURNS so the NEXT wait_turn's resolve_transcript finds
# the new rollout (newer than the bumped marker).
swap_after_slash() {
  local slash="$1"
  # Make sure we've discovered the CURRENT rollout before swapping — its
  # session_id needs to land in the array so the fixture includes it.
  resolve_transcript || true
  local old_transcript="$TRANSCRIPT"
  local old_sid; old_sid="$(daemon_sid "$old_transcript")"
  SESSION_IDS+=("$old_sid")
  SESSION_TRANSCRIPTS+=("$old_transcript")
  echo "[driver] swap ($slash): recorded old session sid=$old_sid ($old_transcript)" >&2

  tmux send-keys -t "$SESSION" -l -- "$slash"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter

  # Bump the marker PAST the old rollout's mtime so discovery skips it.
  # A second granularity collision is possible if the old rollout was
  # just written, so sleep first, then re-touch.
  sleep 1
  touch "$MARKER"
  TRANSCRIPT=""
  UUID=""
  EXPECTED_TURNS=0
  echo "[driver] swap ($slash): marker bumped, awaiting new rollout on next prompt/turn" >&2
  sleep 1
}

step_exit_clean() {
  # codex's TUI binds Ctrl-D to "exit". Ctrl-D triggers a graceful
  # shutdown so codex flushes its rollout and the daemon emits
  # process_exited. Sleep gives codex time to terminate.
  tmux send-keys -t "$SESSION" C-d
  sleep 2
  echo "[driver] exit_clean: sent Ctrl-D to $SESSION" >&2
}

step_resume() {
  # Resume the current codex conversation in a new process lifetime.
  # Exit the running codex cleanly (Ctrl-D), kill its tmux session, then
  # relaunch `codex resume <UUID> --no-alt-screen`. Codex APPENDS to the
  # SAME rollout file (same session_id) across both lifetimes — verified
  # empirically — so this is ONE session with two process lifetimes:
  # CURRENT_UUID / TRANSCRIPT stay unchanged and we do NOT append a
  # second array entry for the resumed half (which would double-list the
  # same rollout path and double-concat the transcript at curate time).
  #
  # Make sure the live rollout is resolved so we have a UUID to resume
  # by. Fall back to `codex resume --last` when the UUID is unknown
  # (e.g. resume with no prior wait_turn).
  resolve_transcript || true
  local resume_uuid="$UUID"
  local saved_transcript="$TRANSCRIPT"

  tmux send-keys -t "$SESSION" C-d
  sleep 2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  local idx=$(( ${#SESSION_IDS[@]} + 1 ))
  SESSION="codex-onboard-$(date +%s)-$$-${idx}"

  # Keep the SAME rollout cached across the relaunch: codex appends to it
  # rather than minting a new one, so resolve_transcript must NOT run
  # again (it would just re-find the same file, but clearing+re-finding
  # risks racing the new process before it reopens the rollout). Keep
  # TRANSCRIPT/UUID/EXPECTED_TURNS as-is.
  if [[ -n "$resume_uuid" ]]; then
    echo "[driver] resume: relaunch codex resume $resume_uuid (same rollout=$saved_transcript)" >&2
    boot_session "$SESSION" codex resume "$resume_uuid" --no-alt-screen
  else
    echo "[driver] resume: UUID unknown — relaunch codex resume --last" >&2
    boot_session "$SESSION" codex resume --last --no-alt-screen
  fi
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
    keys)
      # Raw tmux key sequence (NOT literal text) for navigating picker UIs
      # such as codex's /model two-step selector. Example:
      #   {"type":"keys","keys":"Down Down Enter"}
      ks=$(jq -r '.keys' <<<"$step")
      # shellcheck disable=SC2086 — intentional word-splitting of the key list
      tmux send-keys -t "$SESSION" $ks
      echo "[driver] keys: $ks" >&2
      sleep 0.5
      ;;
    sleep)
      secs=$(jq -r '.seconds // 1' <<<"$step")
      echo "[driver] sleep: ${secs}s" >&2
      sleep "$secs"
      ;;
    reset_session)
      swap_after_slash "/clear"
      ;;
    fork)
      swap_after_slash "/fork"
      ;;
    exit_clean)
      step_exit_clean
      ;;
    resume)
      step_resume
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

# Final session metadata — swap_after_slash only records sessions when it
# tears them down, so the last live session needs an explicit entry here.
SESSION_IDS+=("$(daemon_sid "$TRANSCRIPT")")
SESSION_TRANSCRIPTS+=("$TRANSCRIPT")

sleep 0.5
tmux kill-session -t "$SESSION" 2>/dev/null || true

{
  echo "=== stdout ==="
  cat "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"

echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"

# Primary session = first one (kept for backward-compat with the
# single-session run-cell + curate code paths). Write the daemon-side
# session_id form so run-cell's primary-skip comparison and curate's
# `.session_id` filter both match without depending on the RECORDED_SID
# rewrite.
echo "${SESSION_IDS[0]}" > "$STAGING/session.uuid"
echo "${SESSION_TRANSCRIPTS[0]}" > "$STAGING/transcript.path"

# Multi-session metadata. A single-session run leaves these with one
# entry each — same shape, but run-cell.sh's multi-session branch is a
# no-op when there's only one line.
printf '%s\n' "${SESSION_IDS[@]}" > "$STAGING/session.uuids"
printf '%s\n' "${SESSION_TRANSCRIPTS[@]}" > "$STAGING/transcript.paths"

echo "drive-codex-interactive: $EXIT_REASON (sessions=${#SESSION_IDS[@]}, primary=${SESSION_IDS[0]}, transcript=${SESSION_TRANSCRIPTS[0]})"

case "$EXIT_REASON" in
  ok)            exit 0 ;;
  timeout)       exit 124 ;;
  nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
  *)             exit 1 ;;
esac
