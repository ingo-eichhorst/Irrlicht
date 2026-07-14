#!/usr/bin/env bash
# drive-pi-interactive.sh — drive pi's REPL via tmux, executing a
# step-script (send / wait_turn / interrupt / slash / exit_clean /
# resume). For scenarios that can't be expressed as a single
# `pi --print -p ...` invocation: multi-turn conversations, mid-turn
# interrupts, and clean-exit-then-resume relaunches.
#
# Sister script to drive-pi.sh (headless --print mode). Same staging
# contract: writes driver.log[.stdout], driver.exit-reason,
# transcript.path, session.uuid — PLUS the multi-session lists
# session.uuids / transcript.paths (newline-separated, in order) so
# run-cell.sh / curate union ALL sessions a single run produces into the
# fixture. Mirrors drive-codex-interactive.sh's multi-session contract,
# adapted to pi's FilesUnderRoot transcript-discovery model.
#
# Step types match the aider/claudecode/codex interactive drivers:
#   send       — type text, press Enter
#   slash      — same as send, used for /commands
#   wait_turn  — block until a new {"type":"message","message":{"role":
#                "assistant","stopReason":"stop",...}} line appears in
#                the transcript (signals pi finished the LLM round)
#   interrupt  — Escape the in-flight turn; the cancelled turn will NOT
#                produce a stopReason="stop" line, so don't follow it
#                with wait_turn
#   sleep      — pause N seconds (field: "seconds")
#   exit_clean — Ctrl-D for a graceful shutdown (pi flushes its
#                transcript and the daemon emits process_exited). Pi's
#                banner binds ctrl+d to exit.
#   reset_session — send pi's in-REPL `/new` slash. Pi mints a FRESH
#                <ts>_<uuid2>.jsonl under the SAME project dir
#                (parentSession:null) while keeping the SAME process
#                alive (no relaunch — distinct from resume). The codex
#                /new supersession shape: the NEW session supersedes the
#                OLD; the old UUID-1 file lingers frozen while the daemon
#                removes the old session row via the #169 same-PID cleanup
#                once UUID-2's PID is discovered. Allocates a new slot
#                reusing the same tmux/process and re-arms transcript
#                resolution onto UUID-2, so session.uuids/transcript.paths
#                list BOTH UUIDs and curate captures both rollouts.
#   resume     — Exit the running pi cleanly (Ctrl-D), kill the tmux
#                session, then relaunch `pi --session <transcript>` (or
#                `pi -c` if the transcript is unknown). Pi APPENDS to the
#                SAME <ts>_<uuid>.jsonl transcript across both process
#                lifetimes (it reuses the existing session file rather
#                than minting a new one), so the daemon sees the SAME
#                session_id row come back after the first lifetime exits.
#                This is ONE session (one slot) with two process
#                lifetimes: TRANSCRIPT/UUID/MARKER stay unchanged and we
#                do NOT allocate a new slot (which would double-list the
#                transcript and double-concat it at curate time). Only the
#                tmux session name rotates.
#
# Concurrency (multiple live sessions at once):
#   start_session — launch a NEW concurrent pi REPL WITHOUT tearing down
#                   the active one. Defaults to the same cwd as session 1
#                   (the multiple-sessions-same-cwd case); override with
#                   {"type":"start_session","cwd":"…"}. Both REPLs live in
#                   parallel under separate tmux sessions, each minting
#                   its own <ts>_<uuid>.jsonl under PI_SESSIONS_DIR.
#   any step may carry {"session": N} to switch the active context to
#   session slot N (1-based) before executing — e.g. send a turn to
#   session 1 after start_session moved focus to session 2. A bare
#   {"type":"session","session":N} just switches focus.
#
# Session model: every session lifetime is a 1-based "slot". The initial
# session is slot 1. resume relaunches in place (same slot, same
# transcript file); reset_session (/new) allocates a NEW slot reusing the
# same process (a fresh rollout supersedes the old one); start_session
# allocates a NEW slot in a fresh tmux/process leaving the prior slot
# alive. At the end, ALL slots' session_ids + transcripts are written to
# session.uuids / transcript.paths so run-cell.sh's multi-session
# curation unions them. A single-session run leaves these with one entry
# each — same shape, but run-cell.sh's multi-session branch is a no-op
# when there's only one line.
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

# Per-run CWD so pi creates a session under a unique project dir
# (~/.pi/agent/sessions/<projectdir>/<ts>_<uuid>.jsonl). The dir lives
# inside the staging tree, and the slugified projectdir keeps reruns
# from colliding. run-cell-multi.sh overrides this via $IRRLICHT_ONBOARD_CWD
# so a second, different adapter can share the SAME workspace (the
# cross-adapter multiple-agents-same-workspace rig); matches the
# claudecode/codex interactive drivers.
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Active-session view — the step functions read/write these. They are a
# cache of the active slot's state, kept in sync via save_active /
# load_slot. TRANSCRIPT is the absolute <ts>_<uuid>.jsonl path; UUID is
# the bare session UUID (the first transcript line's .id); SESSION is the
# tmux session name; MARKER gates transcript discovery for this slot
# (resolve_transcript only considers transcripts NEWER than it).
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

# Slot bookkeeping (daemon_sid / save_active / load_slot / alloc_slot) is the
# shared model in lib/drive/slots.sh — byte-identical to codex's except the
# marker filename, set via DRIVE_MARKER_PREFIX (#508 #3).
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
DRIVE_MARKER_PREFIX="$STAGING/.pi-start-marker"
# shellcheck source=lib/drive/slots.sh
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=lib/drive/contracts.sh
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=lib/drive/teardown.sh
source "$_DRIVE_LIB/teardown.sh"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS
# (a subset of its case arms — accepting ≠ producing), read directly by
# recipe-lint so the grammar has ONE owner here, not a parallel manifest.
# tmux-TUI driver. exit_clean (Ctrl-D → process_exited) + resume (relaunch
# `pi --session <transcript>`, reopening the SAME <ts>_<uuid>.jsonl across two
# process lifetimes — one slot, two lifetimes, no new session) for
# session-resume. reset_session sends pi's in-REPL /new (FRESH transcript,
# parentSession:null, same process alive — distinct from resume) following the
# codex /new supersession shape. keys sends raw tmux key names for in-REPL
# picker UIs (e.g. /tree). start_session launches a second concurrent REPL
# without tearing down the active one. Any step may carry {"session":N} to route
# to slot N first.
DRIVE_ELICITS="send slash wait_turn interrupt keys sleep exit_clean resume reset_session start_session session"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

# boot_session brings up a pi REPL in the active slot's tmux session
# running the given argv, then waits for the "Ready" line. Caller
# allocates the slot (alloc_slot) before invoking.
#
# No `pi | tee` pipeline — a pipeline binds Ctrl-C/Ctrl-D to the whole
# process group and would kill pi instead of just the in-flight turn or
# the REPL alone. The pane log is full of ANSI escapes, but the literal
# "Ready" substring is uncorrupted — grep -a treats it as text. Cap 30s.
boot_session() {
  local sess="$SESSION" cwd="${SES_CWD[$ACTIVE]}"
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"
  mkdir -p "$cwd"
  tmux kill-session -t "$sess" 2>/dev/null || true
  tmux new-session -d -s "$sess" -c "$cwd" "$@"
  tmux pipe-pane -t "$sess" -o "cat >> '$slot_stdout'"
  echo "[driver] tmux started: $sess (slot=$ACTIVE, cwd=$cwd, argv: $*)" >&2

  local waited=0
  while [[ $waited -lt 60 ]]; do
    if [[ -f "$slot_stdout" ]] && grep -aq 'Ready' "$slot_stdout" 2>/dev/null; then
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  sleep 1  # extra grace for the input prompt to settle
}

# Pi creates its transcript file under PI_SESSIONS_DIR only after the
# first user message is processed — there's nothing to read at boot.
# Defer transcript/UUID resolution until step_wait_turn (or end of
# script if there are no wait_turns). Discovery finds the newest
# *.jsonl NEWER than this slot's $MARKER.
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
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID, sid=$(daemon_sid "$TRANSCRIPT"))" >&2
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
# (Note: a resumed slot keeps its EXPECTED_TURNS, because pi appends to
# the SAME transcript across both lifetimes — turn_count keeps climbing
# in one file, so wait_turn after resume waits for the cumulative total.)

step_send() {
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: pi never created a transcript under $PI_SESSIONS_DIR" >&2
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
  echo "[driver] interrupt[s$ACTIVE] (Escape, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

step_exit_clean() {
  # Pi's banner binds ctrl+d to "exit". Ctrl-D triggers a graceful
  # shutdown so pi flushes its transcript and the daemon emits
  # process_exited. Sleep gives pi time to terminate. (Ctrl-C would only
  # clear the input buffer on the first press, so it can't be used here.)
  tmux send-keys -t "$SESSION" C-d
  wait_tmux_session_gone "$SESSION" 2
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean[s$ACTIVE]: sent Ctrl-D to $SESSION" >&2
}

step_resume() {
  # Resume the active pi session in a new process lifetime. Exit the
  # running pi cleanly (Ctrl-D), kill its tmux session, then relaunch
  # `pi --session <transcript>` — pi reopens and APPENDS to the SAME
  # <ts>_<uuid>.jsonl across both lifetimes (it does NOT mint a new file),
  # so this is ONE session (one slot) with two process lifetimes:
  # TRANSCRIPT/UUID/EXPECTED_TURNS/MARKER stay unchanged and we do NOT
  # allocate a new slot (which would double-list the transcript and
  # double-concat it at curate time). Only the tmux session name rotates.
  #
  # --session takes a session file path or partial UUID; passing the exact
  # transcript path is the most deterministic (no ambiguity if two sessions
  # share a UUID prefix). The relaunched pi must run in the SAME cwd so the
  # adapter's DiscoverPID (matches process CWD → session working dir) re-binds
  # the new PID to the existing session.
  resolve_transcript || true
  local resume_transcript="$TRANSCRIPT"

  tmux send-keys -t "$SESSION" C-d
  wait_tmux_session_gone "$SESSION" 2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  SESSION="pi-onboard-$(date +%s)-$$-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"

  # Keep the SAME transcript cached across the relaunch: pi appends to it
  # rather than minting a new one, so resolve_transcript must NOT run
  # again (clearing+re-finding risks racing the new process before it
  # reopens the transcript). Keep TRANSCRIPT/UUID/EXPECTED_TURNS as-is.
  if [[ -n "$resume_transcript" ]]; then
    echo "[driver] resume[s$ACTIVE]: relaunch pi --session $resume_transcript (same transcript)" >&2
    boot_session pi --session "$resume_transcript"
  else
    echo "[driver] resume[s$ACTIVE]: transcript unknown — relaunch pi -c (continue-last)" >&2
    boot_session pi -c
  fi
}

# swap_after_slash <slash-text> — shared handler for pi's in-REPL /new
# (reset_session). Pi's /new (interactive-mode.js handleClearCommand →
# runtimeHost.newSession → session-manager.js newSession()) mints a FRESH
# <ts>_<uuid2>.jsonl under the SAME project dir, parentSession:null, while
# keeping the SAME process alive (no exit/relaunch — distinct from resume).
# This is the codex /new supersession shape: the NEW session supersedes
# the OLD; the old UUID-1 file lingers frozen on disk while the daemon
# removes the old session row via the #169 same-PID cleanup once UUID-2's
# PID is discovered. The new rollout materializes LAZILY — only on the
# first post-/new user message.
#
# Mirrors drive-codex-interactive.sh's swap_after_slash: resolve the
# current transcript (so its session_id is recorded in the slot list),
# send the slash, then allocate a NEW slot that reuses the same
# tmux/process. The new slot's fresh marker makes the next wait_turn's
# resolve_transcript find the new <ts>_<uuid2>.jsonl (newest .jsonl NEWER
# than $MARKER) instead of the frozen UUID-1 file.
swap_after_slash() {
  local slash="$1"
  resolve_transcript || true
  local old_tmux="$SESSION"
  local old_cwd="${SES_CWD[$ACTIVE]}"
  save_active
  # The old rollout is frozen; retire the slot but keep it in the list so
  # the epilogue flushes its session_id. The process keeps running (the
  # new slot reuses its tmux), so its tmux is killed exactly once at
  # teardown.
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] swap ($slash): recorded old session sid=$(daemon_sid "$TRANSCRIPT")" >&2

  tmux send-keys -t "$old_tmux" -l -- "$slash"
  sleep 0.3
  tmux send-keys -t "$old_tmux" Enter

  # Allocate the new slot reusing the same tmux/process. alloc_slot mints a
  # fresh marker; sleep first so it sorts strictly after the old rollout's
  # mtime (the find -newer poll has 1s granularity), then re-touch to be
  # safe. EXPECTED_TURNS resets to 0 with the new slot, so the next send +
  # wait_turn pair counts turns in the UUID-2 file from scratch.
  sleep 1
  alloc_slot "$old_tmux" "$old_cwd"
  SES_ALIVE[$ACTIVE]=1
  touch "$MARKER"
  echo "[driver] swap ($slash): new slot #${ACTIVE}, marker bumped, awaiting new rollout" >&2
  sleep 1
}

step_start_session() {
  # Launch a NEW concurrent pi REPL WITHOUT tearing down the active one.
  # The previous slot keeps running (its tmux survives), so the daemon
  # observes both as independent session rows under the same project dir.
  # Defaults to session 1's cwd (the multiple-sessions-same-cwd scenario);
  # pass a directory to launch elsewhere.
  local req_cwd="$1"
  # Claim the current slot's transcript BEFORE spawning a concurrent one.
  # If the prior slot hasn't been resolved (e.g. start_session issued
  # before its wait_turn), its transcript is undiscovered, and a turn
  # still streaming there keeps advancing its mtime past the new slot's
  # just-touched marker — the new slot's resolve_transcript could then
  # bind to the OLD <ts>_<uuid>.jsonl. Resolving here pins the prior slot
  # to its file so the new slot's find -newer poll picks up only the
  # genuinely new transcript.
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot "pi-onboard-$(date +%s)-$$-${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd)" >&2
  boot_session pi
}

# Bring up the first session as slot 1. SCRIPT_JSON's resume step
# relaunches in place; reset_session allocates a further slot (a new
# rollout under the same process); start_session allocates a further
# slot in a fresh tmux/process leaving the prior slot alive.
alloc_slot "pi-onboard-$(date +%s)-$$" "$RUN_CWD"
boot_session pi

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
      # Raw tmux key sequence (NOT literal text) for navigating in-REPL
      # picker UIs such as pi's /tree checkpoint selector (the
      # tree-selector overlay from dist/modes/interactive/components/
      # tree-selector.js). Example:
      #   {"type":"keys","keys":"Down Down Enter"}
      # A picker selection is in-place — NOT a session boundary — so this
      # arm touches NEITHER EXPECTED_TURNS nor the multi-session contract
      # files (session.uuids / transcript.paths).
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
    exit_clean)
      step_exit_clean
      ;;
    reset_session)
      swap_after_slash "/new"
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
# with no wait_turn for that session) gets one last resolution attempt
# before teardown so session.uuids + transcript.paths are populated.
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -z "${SES_TRANSCRIPT[$i]}" ]]; then
    load_slot "$i"
    resolve_transcript || true
    save_active
  fi
done

sleep 0.5

# Shutdown: tear down every still-alive session. Mirrors the single-
# session path — successful scripts end on wait_turn (or interrupt+turn-
# done, or exit_clean) so there's nothing in-flight to interrupt; sending
# /exit or Ctrl-C here would just leave extra noise in the captured pane.
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
# Staging contract: pi's primary session.uuid is the bare first-line UUID for
# fixture-naming parity — run-cell.sh re-maps it to the daemon's filename-stem
# session_id via its transcript_path lookup. The multi-session session.uuids
# list still holds the daemon-side stem (emit_session_contract uses daemon_sid),
# because run-cell.sh feeds that straight into IRRLICHT_EXTRA_SESSION_IDS with
# no per-line re-mapping (lib/drive/contracts.sh).
emit_session_contract "${SES_UUID[1]}"

echo "drive-pi-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=${SES_UUID[1]}, transcript=${SES_TRANSCRIPT[1]})"

drive_exit
