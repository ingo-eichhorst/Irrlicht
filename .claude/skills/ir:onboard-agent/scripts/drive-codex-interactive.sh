#!/usr/bin/env bash
# drive-codex-interactive.sh — drive codex's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash / …). For scenarios
# that can't be expressed as a single `codex exec ...` invocation:
# multi-turn conversations, mid-turn interrupts, /new and /fork
# session swaps, resume relaunches, and multiple concurrent sessions.
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
#   keys       — raw tmux key sequence (Up/Down/Enter/Escape …) for
#                navigating picker UIs such as /model
#   sleep      — pause N seconds (field: "seconds")
#   reset_session — send /new; codex abandons the current conversation
#                and writes the NEXT prompt's turns to a brand-new rollout
#                (new session_id) — the fresh session supersedes the old.
#                The driver records the old session and re-discovers the
#                new rollout on the next wait_turn.
#   fork       — send /fork; codex clones the conversation into a new
#                thread with a fresh session_id. Same new-rollout
#                discovery as reset_session.
#   exit_clean — Ctrl-D for a graceful shutdown (codex flushes its
#                rollout and the daemon emits process_exited).
#   resume     — Ctrl-D the current codex, kill the tmux session, then
#                relaunch `codex resume <UUID> --no-alt-screen`. Codex
#                APPENDS to the SAME rollout (same session_id) across the
#                two process lifetimes (verified empirically), so the
#                session identity is kept unchanged — no new slot is
#                allocated for the resumed half.
#   sigkill    — kill -9 the active session's codex PID (abrupt teardown,
#                no rollout flush). Codex argv is uniform, so we can't
#                pgrep by session like claudecode does with --session-id;
#                instead we target the daemon's PID directly — the process
#                holding the rollout open for writing (see codex/pid.go).
#   restart    — end the active session, start a FRESH one (new rollout,
#                new session_id, fresh cwd). Mirrors the claudecode
#                driver's restart; used to separate session-end variants.
#
# Concurrency (multiple live sessions at once):
#   start_session — launch a NEW codex session WITHOUT tearing down the
#                   active one. Defaults to the same cwd as session 1
#                   (the multiple-sessions-same-cwd case); codex caches
#                   trust per directory so no second trust dialog fires.
#                   Override with {"type":"start_session","cwd":"…"}.
#   any step may carry {"session": N} to switch the active context to
#   session slot N (1-based) before executing — e.g. send a turn to
#   session 1 after start_session moved focus to session 2. A bare
#   {"type":"session","session":N} just switches focus.
#
# Session model: every session lifetime is a 1-based "slot". The initial
# session is slot 1. reset_session/fork/start_session each allocate the
# next slot; reset_session/fork reuse the active codex process (rotate
# its rollout) and retire the old slot, start_session launches a fresh
# process and leaves the previous slot alive. resume relaunches in place
# (same slot, same rollout). At the end, ALL slots' session_ids +
# transcripts are written to session.uuids / transcript.paths so
# run-cell.sh's multi-session curation unions them.
#
# Codex assigns its OWN session UUID per rollout and has no --session-id
# flag; both args are accepted for ABI parity with the other interactive
# drivers. A shared workspace can be forced via $IRRLICHT_ONBOARD_CWD
# (used by run-cell.sh's cross-adapter mode); otherwise each run uses an
# isolated per-run cwd under the staging dir.
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

# Per-run CWD so codex creates sessions under a fresh path, isolating the
# trust dialog to this run. run-cell.sh's cross-adapter mode overrides
# this via $IRRLICHT_ONBOARD_CWD so a second, different adapter can share
# the SAME workspace (multiple-agents-same-workspace).
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Active-session view — the step functions read/write these. They are a
# cache of the active slot's state, kept in sync via save_active /
# load_slot. TRANSCRIPT is the absolute rollout-*.jsonl path; UUID is the
# bare conversation UUID (.payload.id, the `codex resume <UUID>` arg);
# SESSION is the tmux session name; MARKER gates rollout discovery for
# this slot (resolve_transcript only considers rollouts NEWER than it).
SESSION=""
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
MARKER=""

# Per-slot state (1-based; index 0 unused). Each slot is one session
# lifetime. SES_ALIVE[i]=1 while its tmux session is still running.
SES_SESSION=()
SES_TRANSCRIPT=()
SES_UUID=()
SES_EXPECTED=()
SES_MARKER=()
SES_CWD=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0

# Directories codex has already shown (and we accepted) the trust dialog
# for, this run. A second boot in any of these — a concurrent slot in the
# same cwd OR a resume relaunch of the same slot — won't re-prompt, so the
# trust-wait poll must be skipped or it stalls ~15s for a dialog that never
# appears.
TRUSTED_CWDS=()

# Slot bookkeeping (daemon_sid / save_active / load_slot / alloc_slot) is the
# shared model in lib/drive/slots.sh — byte-identical to pi's except the marker
# filename, set via DRIVE_MARKER_PREFIX (#508 #3).
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/lib/drive" && pwd)"
DRIVE_MARKER_PREFIX="$STAGING/.codex-start-marker"
# shellcheck source=lib/drive/slots.sh
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=lib/drive/contracts.sh
source "$_DRIVE_LIB/contracts.sh"

# Has the active slot's cwd already had its trust dialog accepted this
# run? Covers BOTH a concurrent second slot in the same cwd AND a resume
# relaunch of the same slot (codex won't re-prompt for a dir it already
# trusts), so the boot trust-wait can be skipped.
cwd_already_trusted() {
  local c="${SES_CWD[$ACTIVE]}" t
  for t in ${TRUSTED_CWDS[@]+"${TRUSTED_CWDS[@]}"}; do
    [[ "$t" == "$c" ]] && return 0
  done
  return 1
}

# boot_session brings up a codex TUI in the active slot's tmux session
# running the given argv, accepts the trust dialog (unless this slot's
# cwd was already trusted by an earlier slot this run), waits for the
# "OpenAI Codex" banner, and waits out the "Booting MCP" phase. Caller
# allocates the slot (alloc_slot) before invoking.
#
# Launch/boot notes:
#   --no-alt-screen keeps codex in inline mode so its output is
#   capturable via tmux pipe-pane (alt-screen would clear the screen on
#   every redraw and yield mostly noise).
#
#   Per-slot stdout: each slot pipes to $DRIVER_LOG.stdout.$ACTIVE. A
#   single shared file would interleave two concurrent panes' TUI
#   refreshes and confuse banner / trust detection.
#
#   Trust dialog: codex shows "Do you trust the contents of this
#   directory?" on first encounter with a directory. The pipe-pane LOG
#   splits that string across cursor-positioning escapes, so a literal
#   grep on the LOG misses it — poll the LIVE pane via capture-pane
#   instead, which renders the text contiguously.
#
#   Banner: "OpenAI Codex (vN.N.N)" renders contiguously in the LOG.
#   Generous cap (90s) because codex may auto-install npm updates on
#   launch.
#
#   Booting MCP: codex then spends ~5-15s booting MCP servers; keystrokes
#   typed during this phase have their Enter silently swallowed. Poll the
#   LIVE pane until "Booting MCP" is gone.
boot_session() {
  local sess="$SESSION" cwd="${SES_CWD[$ACTIVE]}"
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"
  mkdir -p "$cwd"
  tmux kill-session -t "$sess" 2>/dev/null || true
  tmux new-session -d -s "$sess" -c "$cwd" "$@"
  tmux pipe-pane -t "$sess" -o "cat >> '$slot_stdout'"
  echo "[driver] tmux started: $sess (slot=$ACTIVE, cwd=$cwd, argv: $*)" >&2

  # codex caches trust per-directory, so a second boot in an
  # already-trusted cwd (concurrent slot OR resume relaunch) never sees the
  # prompt — skip the wait so we don't stall ~15s for a dialog that will
  # never appear.
  if cwd_already_trusted; then
    echo "[driver] trust: cwd already trusted this run — skipping prompt" >&2
  else
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
    # Remember this cwd so a later resume/concurrent boot here skips the poll.
    TRUSTED_CWDS+=("$cwd")
  fi

  local WAITED=0
  while [[ $WAITED -lt 180 ]]; do
    if [[ -f "$slot_stdout" ]] && grep -aq 'OpenAI Codex' "$slot_stdout" 2>/dev/null; then
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

# transcript_claimed reports whether an absolute rollout path is already
# bound to a DIFFERENT slot, so concurrent discovery never double-binds
# the same rollout when per-slot markers collide at 1s mtime granularity.
transcript_claimed() {
  local p="$1" i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ $i -eq $ACTIVE ]] && continue
    [[ "${SES_TRANSCRIPT[$i]}" == "$p" ]] && return 0
  done
  return 1
}

# Codex creates its rollout file under CODEX_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Discovery finds the newest rollout-*.jsonl NEWER than this slot's
# $MARKER that isn't already bound to another slot; after a /new or
# /fork (which bump the marker) the prior rollout is excluded so the new
# one is picked up. With concurrent sessions each slot resolves on its
# first wait_turn — before the next session is booted — and caches the
# result, so later focus switches reuse the bound path.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    local candidate=""
    local f
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      transcript_claimed "$f" && continue
      candidate="$f"
    done < <(find "$CODEX_SESSIONS_DIR" -maxdepth 5 -type f \
                  -name 'rollout-*.jsonl' -newer "$MARKER" 2>/dev/null | sort)
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      UUID="$(head -n1 "$TRANSCRIPT" | jq -r '.payload.id // empty' 2>/dev/null || true)"
      [[ -n "$UUID" ]] || { TRANSCRIPT=""; sleep 0.5; continue; }
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID, sid=$(daemon_sid "$TRANSCRIPT"))" >&2
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
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: codex never created a rollout under $CODEX_SESSIONS_DIR" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected ≥ $EXPECTED_TURNS)" >&2
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
  echo "[driver] interrupt[s$ACTIVE] (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

# swap_after_slash <slash-text> — shared handler for /new (reset_session)
# and /fork (fork). Both abandon the current rollout and cause codex to
# write subsequent turns to a NEW rollout with a fresh session_id, in the
# SAME process:
#   /new   is LAZY  — the new rollout materializes only on the first
#                     post-reset user message.
#   /fork  is EAGER — the new rollout materializes the instant the
#                     command runs (carrying replayed pre-fork history).
# Either way: resolve the current rollout (so its session_id is recorded
# in the slot list), send the slash, then allocate a NEW slot that reuses
# the same tmux/process. The new slot's fresh marker makes the next
# wait_turn's resolve_transcript find the new rollout.
swap_after_slash() {
  local slash="$1"
  resolve_transcript || true
  local old_tmux="$SESSION"
  local old_cwd="${SES_CWD[$ACTIVE]}"
  save_active
  # The old rollout is frozen; retire the slot but keep it in the list so
  # the epilogue flushes its session_id. The process keeps running (the
  # new slot reuses its tmux), so it is killed exactly once at teardown.
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] swap ($slash): recorded old session sid=$(daemon_sid "$TRANSCRIPT")" >&2

  tmux send-keys -t "$old_tmux" -l -- "$slash"
  sleep 0.3
  tmux send-keys -t "$old_tmux" Enter

  # Allocate the new slot reusing the same tmux/process. alloc_slot mints
  # a fresh marker; sleep first so it sorts strictly after the old
  # rollout's mtime (1s granularity), then re-touch to be safe.
  sleep 1
  alloc_slot "$old_tmux" "$old_cwd"
  SES_ALIVE[$ACTIVE]=1
  touch "$MARKER"
  echo "[driver] swap ($slash): new slot #${ACTIVE}, marker bumped, awaiting new rollout" >&2
  sleep 1
}

step_exit_clean() {
  # codex's TUI binds Ctrl-D to "exit". Ctrl-D triggers a graceful
  # shutdown so codex flushes its rollout and the daemon emits
  # process_exited. Sleep gives codex time to terminate.
  tmux send-keys -t "$SESSION" C-d
  sleep 2
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean: sent Ctrl-D to $SESSION" >&2
}

step_resume() {
  # Resume the active codex conversation in a new process lifetime.
  # Exit the running codex cleanly (Ctrl-D), kill its tmux session, then
  # relaunch `codex resume <UUID> --no-alt-screen`. Codex APPENDS to the
  # SAME rollout file (same session_id) across both lifetimes — verified
  # empirically — so this is ONE session (one slot) with two process
  # lifetimes: TRANSCRIPT/UUID/MARKER stay unchanged and we do NOT
  # allocate a new slot (which would double-list the rollout and
  # double-concat the transcript at curate time). Only the tmux session
  # name rotates.
  resolve_transcript || true
  local resume_uuid="$UUID"
  local saved_transcript="$TRANSCRIPT"

  tmux send-keys -t "$SESSION" C-d
  sleep 2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  SESSION="codex-onboard-$(date +%s)-$$-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"

  # Keep the SAME rollout cached across the relaunch: codex appends to it
  # rather than minting a new one, so resolve_transcript must NOT run
  # again (clearing+re-finding risks racing the new process before it
  # reopens the rollout). Keep TRANSCRIPT/UUID/EXPECTED_TURNS as-is.
  if [[ -n "$resume_uuid" ]]; then
    echo "[driver] resume[s$ACTIVE]: relaunch codex resume $resume_uuid (same rollout=$saved_transcript)" >&2
    boot_session codex resume "$resume_uuid" --no-alt-screen
  else
    echo "[driver] resume[s$ACTIVE]: UUID unknown — relaunch codex resume --last" >&2
    boot_session codex resume --last --no-alt-screen
  fi
}

step_sigkill() {
  # kill -9 the active slot's codex process — abrupt teardown with no
  # rollout flush (the SIGKILL counterpart to exit_clean's graceful
  # Ctrl-D). Unlike drive-claudecode-interactive.sh, which pgreps
  # `--session-id <uuid>` out of argv, codex's argv is uniform (every
  # session is just `codex --no-alt-screen`), so there is no per-session
  # argv marker to match. Instead target exactly the process the daemon
  # tracks: codex holds its rollout .jsonl open for writing for the whole
  # session lifetime, and the daemon discovers the PID as that write-FD
  # holder (codex/pid.go → DiscoverPIDByTranscriptWriter, via lsof).
  # Mirroring that lookup here guarantees the SIGKILL lands on the daemon's
  # PID, so process_exited fires.
  resolve_transcript || true
  local pid=""
  if [[ -n "$TRANSCRIPT" ]]; then
    # Same lsof write-FD match as the daemon: COMMAND PID USER FD …; the
    # FD column ends in 'w' for a writer.
    pid=$(lsof "$TRANSCRIPT" 2>/dev/null | awk 'NR>1 && $4 ~ /w$/ {print $2; exit}')
  fi
  # Fallback: the codex process in this slot's tmux pane. Resolve the codex
  # descendant of the pane (in case tmux wrapped the command in a shell) so
  # the SIGKILL can't merely orphan codex — an orphaned codex keeps writing
  # and the daemon would never observe process_exited.
  if [[ -z "$pid" ]]; then
    local pane_pid
    pane_pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [[ -n "$pane_pid" ]]; then
      pid=$(pgrep -x codex -P "$pane_pid" 2>/dev/null | head -1)
      [[ -z "$pid" ]] && pid="$pane_pid"
    fi
  fi
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
  else
    echo "[driver] sigkill[s$ACTIVE]: no codex PID found (transcript=${TRANSCRIPT:-none}, session=$SESSION)" >&2
  fi
  SES_ALIVE[$ACTIVE]=0
  # Leave the dead tmux pane for teardown — the kill alone produces
  # process_exited.
  sleep 1
}

step_restart() {
  # End the active session and start a FRESH codex (new rollout, new
  # session_id, fresh cwd). Mirrors drive-claudecode-interactive.sh's
  # restart: used between session-end variants so each lands as its own
  # session row, separated by a grey gap where no session is alive. By the
  # time restart runs the active process is usually already gone (an
  # exit_clean or sigkill preceded it); retire the slot regardless but keep
  # it in the list so the epilogue flushes its session_id. A fresh cwd
  # keeps each variant's rollout cleanly separated and gives it its own
  # trust state (codex caches trust per directory).
  resolve_transcript || true
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "codex-onboard-$(date +%s)-$$-${idx}" "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${RUN_CWD}-${idx})" >&2
  boot_session codex --no-alt-screen
}

step_start_session() {
  # Launch a NEW concurrent codex session WITHOUT tearing down the active
  # one. The previous session keeps running (its tmux survives), so the
  # daemon observes both as independent session rows. Defaults to session
  # 1's cwd (the same-cwd scenario); pass a directory to launch elsewhere.
  local req_cwd="$1"
  # Claim the current slot's rollout BEFORE spawning a concurrent one. If
  # the prior slot hasn't been resolved (e.g. start_session issued before
  # its wait_turn) its rollout is unclaimed, and a turn still streaming
  # there keeps advancing its mtime past the new slot's just-touched
  # marker — the new slot's resolve_transcript could then bind to the OLD
  # rollout. Resolving here marks it claimed so transcript_claimed excludes
  # it from the new slot's discovery.
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot "codex-onboard-$(date +%s)-$$-${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd)" >&2
  boot_session codex --no-alt-screen
}

# Bring up the first session as slot 1. SCRIPT_JSON's reset_session/fork/
# start_session steps allocate further slots; resume relaunches in place.
alloc_slot "codex-onboard-$(date +%s)-$$" "$RUN_CWD"
boot_session codex --no-alt-screen

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
      echo "[driver] switch -> session slot $tgt (uuid=$UUID)" >&2
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
      # Raw tmux key sequence (NOT literal text) for navigating picker UIs
      # such as codex's /model two-step selector. Example:
      #   {"type":"keys","keys":"Down Down Enter"}
      ks=$(jq -r '.keys' <<<"$step")
      # shellcheck disable=SC2086 — intentional word-splitting of the key list
      tmux send-keys -t "$SESSION" $ks
      echo "[driver] keys[s$ACTIVE]: $ks" >&2
      sleep 0.5
      ;;
    sleep)
      secs=$(jq -r '.seconds // 1' <<<"$step")
      echo "[driver] sleep: ${secs}s" >&2
      sleep "$secs"
      ;;
    reset_session)
      swap_after_slash "/new"
      ;;
    fork)
      swap_after_slash "/fork"
      ;;
    exit_clean)
      step_exit_clean
      ;;
    sigkill)
      step_sigkill
      ;;
    restart)
      step_restart
      ;;
    resume)
      step_resume
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
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  fi
done

{
  echo "=== stdout ==="
  for (( i = 1; i <= N_SLOTS; i++ )); do
    if [[ -f "$DRIVER_LOG.stdout.$i" ]]; then
      echo "--- session slot $i (sid=$(daemon_sid "${SES_TRANSCRIPT[$i]}")) ---"
      cat "$DRIVER_LOG.stdout.$i" 2>/dev/null || true
      echo
    fi
  done
  echo
  echo "=== exit reason: $EXIT_REASON ==="
} > "$DRIVER_LOG"
# Staging contract: primary session.uuid is codex's daemon-side session_id
# (rollout filename stem) so run-cell's primary-skip comparison and curate's
# `.session_id` filter both match. emit_session_contract handles the combined
# stdout, exit-reason, and the multi-session lists (lib/drive/contracts.sh).
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"

echo "drive-codex-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=$(daemon_sid "${SES_TRANSCRIPT[1]}"), transcript=${SES_TRANSCRIPT[1]})"

drive_exit
