#!/usr/bin/env bash
# drive-gemini-cli-interactive.sh — drive gemini-cli's REPL via tmux, executing
# a step-script (send / wait_turn / slash / …). For scenarios that can't be
# expressed as a single headless invocation: multi-turn conversations, the
# /rewind picker, /clear resets, clean/abrupt teardowns, and resume relaunches.
#
# Slot model + staging contract are SHARED with the codex/pi interactive
# drivers (replaydata/_lib/drive/{slots.sh,contracts.sh}); only the three
# AGENT-SPECIFIC SEAMS differ — launch, transcript resolution, and turn
# counting. Gemini, like codex, mints its OWN session id (no --session-id on
# the happy path) and the daemon keys the session on the transcript FILE
# BASENAME, which is exactly what `daemon_sid` returns — so the shared slot
# bookkeeping carries over unchanged.
#
# Step types:
#   send / slash   — type text + Enter (slash is the same keystrokes)
#   wait_turn      — block until the agent finishes the turn (SEAM 3)
#   keys           — raw tmux key sequence (arrow-key pickers, e.g. /rewind)
#   sleep          — pause N seconds
#   reset_session  — /clear: same process, NEW session id + new chats jsonl
#   restart        — end the session, start a FRESH one (new id, new cwd)
#   resume         — relaunch the SAME id+cwd via `gemini --resume <UUID>`
#                    (gemini APPENDS to the original chats/session-*.jsonl, so
#                    the same basename-keyed session_id reappears)
#   sigkill        — kill -9 the active session's launcher PID
#   exit_clean     — Ctrl-D graceful shutdown
#   start_session  — launch a concurrent session without tearing the first down
#   session        — switch the active slot (carried as {"session": N})
#
# --- Gemini transport (FilesUnderRoot) --------------------------------------
# Gemini writes one append-only JSONL transcript per session under
# ~/.gemini/tmp/<project>/chats/session-<ts>-<id8>.jsonl, where <project> is the
# basename of the launch cwd. Line 1 is a session header carrying sessionId (a
# UUID); subsequent lines are bare message objects ({id,type:"user"|"gemini",
# content,toolCalls,…}) or {"$set":{…}} mutation envelopes. The Go adapter
# (core/adapters/inbound/agents/geminicli/parser.go) settles to ready on a
# "gemini" message that carries non-empty text and opens NO tool calls — there
# is no explicit turn_done marker. wait_turn mirrors that classification.
#
# Usage:
#   drive-gemini-cli-interactive.sh <staging-dir> <session-uuid-ignored> \
#       <timeout-seconds> <settings-path-ignored> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-gemini-cli-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# $2 (preferred-uuid) and $4 (settings-path) are accepted for ABI parity with
# the other interactive drivers; gemini mints its own session id and reads its
# auth from the per-cwd .gemini/settings.json this driver writes, so both unused.
TIMEOUT_S="$3"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

GEMINI_HOME="${GEMINI_DIR:-$HOME/.gemini}"
GEMINI_CHATS_ROOT="$GEMINI_HOME/tmp"
GEMINI_BIN="${GEMINI_BIN:-gemini}"

# Per-run CWD so gemini writes its transcript under a unique
# ~/.gemini/tmp/<project>/chats/ dir; also keeps the trust-folder prompt
# isolated to this run's path. run-cell.sh's cross-adapter mode overrides
# this via $IRRLICHT_ONBOARD_CWD so a second adapter shares the SAME workspace.
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Active-session view — the step functions read/write these. They are a cache of
# the active slot's state, kept in sync via save_active / load_slot. TRANSCRIPT
# is the absolute chats/session-*.jsonl path; UUID is the in-file sessionId
# header (the `gemini --resume <UUID>` arg); SESSION is the tmux session name;
# MARKER gates rollout discovery for this slot (resolve_transcript only
# considers chats files NEWER than it).
SESSION=""
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
MARKER=""

# Per-slot state (1-based; index 0 unused). Each slot is one session lifetime.
SES_SESSION=()
SES_TRANSCRIPT=()
SES_UUID=()
SES_EXPECTED=()
SES_MARKER=()
SES_CWD=()
SES_ALIVE=()
N_SLOTS=0
ACTIVE=0

# Directories whose trust-folder prompt was already accepted this run. A second
# boot in any of these (concurrent slot OR resume relaunch) won't re-prompt.
TRUSTED_CWDS=()

# Slot bookkeeping (daemon_sid / save_active / load_slot / alloc_slot) is the
# shared model in _lib/drive/slots.sh. daemon_sid (basename minus ".jsonl") is
# EXACTLY the gemini fswatcher session_id (extractSessionID), so the shared
# epilogue lists the right ids with no gemini-specific override.
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
DRIVE_MARKER_PREFIX="$STAGING/.gemini-marker"
# shellcheck source=../../_lib/drive/slots.sh
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=../../_lib/drive/contracts.sh
source "$_DRIVE_LIB/contracts.sh"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Every listed primitive
# has a working seam below; a primitive whose arm is still a `not_implemented`
# stub must NOT appear here, so recipe-lint flags a recipe needing it as a
# semantic_gap (exit 4) before recording.
DRIVE_ELICITS="send slash wait_turn keys sleep restart resume reset_session sigkill exit_clean start_session session"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

# --- API-key auth (shared by every launch) -----------------------------------
# Force API-key auth in the launch cwd: a workspace .gemini/settings.json
# overrides the user's global auth type, so recordings draw on the
# GEMINI_API_KEY quota pool (separate from OAuth, unaffected by the 2026-06-18
# unpaid-tier shutdown) and never silently fall back to the user's OAuth login.
# With selectedType=gemini-api-key, gemini hard-errors when no key is present.
require_api_key() {
  if [[ -z "${GEMINI_API_KEY:-}" ]]; then
    echo "[driver] GEMINI_API_KEY not set — export it (e.g. 'set -a; . .build/.env; set +a') before recording" >&2
    EXIT_REASON="nonzero(2)"; exit 1
  fi
}
write_auth_settings() { # <cwd>
  mkdir -p "$1/.gemini"
  printf '{ "security": { "auth": { "selectedType": "gemini-api-key" } } }\n' > "$1/.gemini/settings.json"
}

# Has the active slot's cwd already had its trust prompt accepted this run?
cwd_already_trusted() {
  local c="${SES_CWD[$ACTIVE]}" t
  for t in ${TRUSTED_CWDS[@]+"${TRUSTED_CWDS[@]}"}; do
    [[ "$t" == "$c" ]] && return 0
  done
  return 1
}

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux ------------------------
# boot_session brings up a gemini TUI in the active slot's tmux session running
# the given argv (`gemini -y …`), accepts the trust-folder prompt (unless this
# slot's cwd was already trusted this run), and lets the input prompt settle.
# Caller allocates the slot (alloc_slot) before invoking.
#
# A pre-existing tmux server hands new sessions ITS stale environment, not the
# driver's — so inject the API key + trust into the NEW session's env with `-e`
# (a bare `export GEMINI_API_KEY` would not reach gemini and interactive
# api-key auth would block on a "paste your API key" prompt).
boot_session() {
  local sess="$SESSION" cwd="${SES_CWD[$ACTIVE]}"
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"
  mkdir -p "$cwd"
  write_auth_settings "$cwd"
  tmux kill-session -t "$sess" 2>/dev/null || true
  tmux new-session -d -s "$sess" -x 200 -y 50 -c "$cwd" \
    -e "GEMINI_API_KEY=$GEMINI_API_KEY" -e "GEMINI_CLI_TRUST_WORKSPACE=true" \
    "$@" \
    >>"$slot_stdout" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch gemini under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$sess" -o "cat >> '$slot_stdout'" 2>/dev/null || true
  echo "[driver] tmux started: $sess (slot=$ACTIVE, cwd=$cwd, argv: $*)" >&2

  if cwd_already_trusted; then
    echo "[driver] trust: cwd already trusted this run — skipping prompt" >&2
  else
    local waited=0
    while (( waited < 20 )); do
      if tmux capture-pane -t "$sess" -p -S -40 2>/dev/null | grep -qiE 'trust|do you trust'; then
        tmux send-keys -t "$sess" Enter   # default highlighted option trusts the folder
        echo "[driver] accepted trust-folder prompt" >&2
        break
      fi
      sleep 0.5; waited=$((waited + 1))
    done
    TRUSTED_CWDS+=("$cwd")
  fi

  # Wait for the input prompt to actually be ready before the first send. On a
  # plain boot the footer's "Type your message" appears within ~1-2 s; on a
  # `--resume` relaunch gemini first replays history under a "Resuming
  # session..." spinner, and keystrokes typed during that phase have their text
  # (and Enter) silently swallowed — the send is lost and wait_turn then times
  # out. Poll the LIVE pane until the input box is up AND the resume spinner is
  # gone (or fall through after the cap so a banner-text mismatch can't deadlock).
  local waited=0
  while (( waited < 60 )); do
    local pane; pane="$(tmux capture-pane -t "$sess" -p -S -20 2>/dev/null || true)"
    if grep -q 'Type your message' <<<"$pane" && ! grep -q 'Resuming session' <<<"$pane"; then
      break
    fi
    sleep 0.5; waited=$((waited + 1))
  done
  sleep 1   # extra grace for the input prompt to settle before the first send
}

# transcript_claimed reports whether an absolute chats path is already bound to
# a DIFFERENT slot, so concurrent discovery never double-binds the same file.
transcript_claimed() {
  local p="$1" i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ $i -eq $ACTIVE ]] && continue
    [[ "${SES_TRANSCRIPT[$i]}" == "$p" ]] && return 0
  done
  return 1
}

# --- SEAM 3: resolve the transcript Gemini just created ----------------------
# Gemini creates the per-session JSONL only after the first user message lands,
# so there's nothing to read at boot. Find the newest session-*.jsonl under
# ~/.gemini/tmp/<project>*/chats/ created AFTER this slot's $MARKER (so a /clear
# or restart, which bumps the marker, excludes the prior file) that isn't bound
# to another slot, and harvest the header's sessionId. Caches the path once
# resolved.
#
# Gemini maps cwd → ~/.gemini/tmp/<basename>/chats, but DEDUPES with a -N suffix
# when the basename collides (every recording's staging cwd is named "cwd", so
# all but the first land in cwd-1, cwd-2, …). Search all <project>* dirs and take
# the newest file by mtime (-exec ls -t is BSD-safe; macOS find has no -printf).
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  local project; project="$(basename "${SES_CWD[$ACTIVE]}")"
  for _ in $(seq 1 60); do
    # `ls -t` lists newest first; take the FIRST unclaimed (= newest unclaimed).
    local candidate="" f
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      transcript_claimed "$f" && continue
      candidate="$f"; break
    done < <(find "$GEMINI_CHATS_ROOT"/"$project"*/chats -maxdepth 1 -type f \
                  -name 'session-*.jsonl' -newer "$MARKER" \
                  -exec ls -t {} + 2>/dev/null)
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      UUID="$(head -n1 "$candidate" | jq -r '.sessionId // empty' 2>/dev/null || true)"
      if [[ -n "$UUID" ]]; then
        TRANSCRIPT="$candidate"
        echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (sessionId=$UUID, sid=$(daemon_sid "$TRANSCRIPT"))" >&2
        return 0
      fi
    fi
    sleep 0.5
  done
  return 1
}

# --- SEAM 2: count completed turns -------------------------------------------
# Mirror core/adapters/inbound/agents/geminicli/parser.go: a bare "gemini"
# message with non-empty text and NO tool calls is the turn's last word
# (turn_done). Streaming placeholders (empty content) and tool-calling messages
# keep the session working and are excluded. $set envelopes carry no "type" and
# are skipped by the select.
turn_count() {
  [[ -f "$TRANSCRIPT" ]] || { echo 0; return; }
  jq -r 'select(.type=="gemini"
                and ((.content // "") | gsub("\\s";"") | length) > 0
                and ((.toolCalls // []) | length) == 0) | "x"' \
    "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
}

step_send() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3   # let Gemini's Ink input render before Enter, so Enter isn't dropped
  tmux send-keys -t "$SESSION" Enter
  # A slash command (e.g. /rewind, /clear) is a LOCAL REPL action — it opens a
  # picker or rotates state but produces NO `gemini` message, so it must NOT bump
  # the turn counter (turn_count only counts gemini messages). Counting it would
  # make the next real wait_turn over-wait by one and time out. Regular prompts
  # bump as usual.
  if [[ "$1" == /* ]]; then
    echo "[driver] send[s$ACTIVE]: ${1:0:60} (slash command — no turn)" >&2
  else
    EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
    echo "[driver] send[s$ACTIVE]: ${1:0:60} (expecting turn $EXPECTED_TURNS)" >&2
  fi
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: gemini never created a transcript under $GEMINI_CHATS_ROOT" >&2
    EXIT_REASON="readiness_timeout"; return 1
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
  EXIT_REASON="timeout"; return 1
}

# step_keys sends a raw tmux key sequence (NOT literal text) for navigating
# picker UIs like /rewind's RewindViewer — Up/Down/Enter/Escape. Each
# space-separated token becomes one tmux key event; no implicit Enter.
#   {"type":"keys","keys":"Down"}   {"type":"keys","keys":"Down Down Enter"}
step_keys() { # <keys>
  # shellcheck disable=SC2086 — intentional word-splitting of the key list
  tmux send-keys -t "$SESSION" $1
  echo "[driver] keys[s$ACTIVE]: $1" >&2
  sleep 0.3
}

step_exit_clean() {
  # Gemini's Ink TUI exits gracefully on Ctrl-D. The OS terminates the
  # `node .../bin/gemini` launcher and the daemon's process scanner emits
  # process_exited. Sleep gives gemini time to flush final transcript lines.
  tmux send-keys -t "$SESSION" C-d
  sleep 2
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean[s$ACTIVE]: sent Ctrl-D to $SESSION" >&2
}

# Find the launcher PID the daemon binds: the lowest-PID `bin/gemini` process
# in this slot's cwd (matching core/adapters/inbound/agents/geminicli/pid.go,
# which calls DiscoverPIDByCWDAndCmdLine on `bin/gemini` and picks lowestPID,
# excluding the --max-old-space-size heap-bump worker). Killing that launcher is
# what makes process_exited fire; the heap worker dies with its parent.
launcher_pid() {
  local cwd="${SES_CWD[$ACTIVE]}" best="" p
  for p in $(pgrep -f 'bin/gemini' 2>/dev/null); do
    # Match the process cwd (BSD lsof -a -p; the daemon matches on cwd too).
    local pcwd
    pcwd="$(lsof -a -p "$p" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
    [[ "$pcwd" == "$cwd" ]] || continue
    # Skip the heap-bump worker (re-exec carries --max-old-space-size in argv).
    ps -o command= -p "$p" 2>/dev/null | grep -q -- '--max-old-space-size' && continue
    if [[ -z "$best" || "$p" -lt "$best" ]]; then best="$p"; fi
  done
  echo "$best"
}

step_sigkill() {
  resolve_transcript || true
  local pid; pid="$(launcher_pid)"
  # Fallback: the gemini process descended from this slot's tmux pane, so the
  # SIGKILL can't merely orphan gemini (an orphan keeps the cwd alive and the
  # daemon would never observe process_exited).
  if [[ -z "$pid" ]]; then
    local pane_pid
    pane_pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [[ -n "$pane_pid" ]]; then
      pid=$(pgrep -f 'bin/gemini' -P "$pane_pid" 2>/dev/null | head -1)
      [[ -z "$pid" ]] && pid="$pane_pid"
    fi
  fi
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
  else
    echo "[driver] sigkill[s$ACTIVE]: no gemini PID found (cwd=${SES_CWD[$ACTIVE]}, session=$SESSION)" >&2
  fi
  SES_ALIVE[$ACTIVE]=0
  # Leave the dead tmux pane for teardown — the kill alone produces process_exited.
  sleep 1
}

step_restart() {
  # End the active session and start a FRESH gemini (new session id, new chats
  # file, fresh cwd). Used between session-end variants so each lands as its own
  # session row, separated by a grey gap where no session is alive. By the time
  # restart runs the active process is usually already gone (an exit_clean or
  # sigkill preceded it); retire the slot regardless but keep it in the list so
  # the epilogue flushes its session_id. A fresh cwd keeps each variant's chats
  # file cleanly separated and gives it its own trust state.
  resolve_transcript || true
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "geminidrv-$$-$(date +%s)-${idx}" "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${RUN_CWD}-${idx})" >&2
  boot_session "$GEMINI_BIN" -y
}

step_resume() {
  # Resume the active gemini conversation in a new process lifetime. Exit the
  # running gemini cleanly (Ctrl-D), kill its tmux session, then relaunch
  # `gemini -y --resume <UUID>` in the SAME cwd. Gemini APPENDS to the SAME
  # chats/session-*.jsonl (same basename → same daemon session_id) across both
  # lifetimes — so this is ONE session (one slot) with two process lifetimes:
  # TRANSCRIPT/UUID/MARKER stay unchanged and we do NOT allocate a new slot
  # (which would double-list the chats file at curate time). Only the tmux
  # session name rotates; the cwd is reused so the daemon rebinds the new PID to
  # the same session (cwd harvested from <session_context>).
  resolve_transcript || true
  local resume_uuid="$UUID"
  local saved_transcript="$TRANSCRIPT"

  tmux send-keys -t "$SESSION" C-d
  sleep 2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  SESSION="geminidrv-$$-$(date +%s)-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"

  # Keep the SAME transcript cached across the relaunch: gemini appends to it
  # rather than minting a new file, so resolve_transcript must NOT run again
  # (clearing + re-finding could race the new process before it reopens the
  # chats file). Keep TRANSCRIPT/UUID/EXPECTED_TURNS as-is.
  if [[ -n "$resume_uuid" ]]; then
    echo "[driver] resume[s$ACTIVE]: relaunch gemini --resume $resume_uuid (same chats=$saved_transcript)" >&2
    boot_session "$GEMINI_BIN" -y --resume "$resume_uuid"
  else
    echo "[driver] resume[s$ACTIVE]: UUID unknown — relaunch gemini --resume latest" >&2
    boot_session "$GEMINI_BIN" -y --resume latest
  fi
}

step_reset_session() {
  # /clear (alt /new) abandons the current conversation and starts a fresh one
  # IN THE SAME PROCESS: gemini mints a new session id and materializes a NEW
  # session-<ts>-<id8>.jsonl under the same chats/ dir; the old file stops being
  # appended. The driver records the old slot, then allocates a NEW slot that
  # reuses the same tmux/process/cwd but points at the new chats file (found by
  # resolve_transcript on the next wait_turn, gated by the fresh marker).
  resolve_transcript || true
  local old_tmux="$SESSION"
  local old_cwd="${SES_CWD[$ACTIVE]}"
  save_active
  # The old session is frozen; retire the slot but keep it in the list so the
  # epilogue flushes its session_id. The process keeps running (the new slot
  # reuses its tmux), so it is killed exactly once at teardown.
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] reset_session: recorded old session sid=$(daemon_sid "$TRANSCRIPT")" >&2

  tmux send-keys -t "$old_tmux" -l -- "/clear"
  sleep 0.3
  tmux send-keys -t "$old_tmux" Enter

  # Allocate the new slot reusing the same tmux/process. alloc_slot mints a fresh
  # marker; sleep first so it sorts strictly after the old chats file's mtime
  # (1s granularity), then re-touch to be safe.
  sleep 1
  alloc_slot "$old_tmux" "$old_cwd"
  SES_ALIVE[$ACTIVE]=1
  touch "$MARKER"
  echo "[driver] reset_session: new slot #${ACTIVE}, marker bumped, awaiting new chats file" >&2
  sleep 1
}

step_start_session() {
  # Launch a NEW concurrent gemini session WITHOUT tearing down the active one.
  # The previous session keeps running (its tmux survives), so the daemon
  # observes both as independent session rows. Defaults to session 1's cwd (the
  # same-cwd scenario); pass a directory to launch elsewhere.
  local req_cwd="$1"
  resolve_transcript || true   # claim the current slot's chats file first
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  alloc_slot "geminidrv-$$-$(date +%s)-${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd)" >&2
  boot_session "$GEMINI_BIN" -y
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down for any slot that
# was started.
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

require_api_key

# Bring up the first session as slot 1. SCRIPT_JSON's reset_session/restart/
# start_session steps allocate further slots; resume relaunches in place.
alloc_slot "geminidrv-$$-$(date +%s)" "$RUN_CWD"
boot_session "$GEMINI_BIN" -y

# Iterate steps. EXIT_REASON / array updates persist via the parent shell
# (process substitution feeds the loop — the body is NOT subshelled).
STEP_OK=true
while read -r step; do
  if ! $STEP_OK; then break; fi
  type=$(jq -r '.type' <<<"$step")

  # Optional inline session target: switch the active context to slot N before
  # executing the step. start_session is exempt (it allocates its own slot).
  tgt=$(jq -r '.session // empty' <<<"$step")
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (uuid=$UUID)" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"; STEP_OK=false; continue
    fi
  fi

  case "$type" in
    send|slash)      step_send "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       step_wait_turn || STEP_OK=false ;;
    keys)            step_keys "$(jq -r '.keys' <<<"$step")" ;;
    sleep)           secs=$(jq -r '.seconds // 1' <<<"$step"); echo "[driver] sleep: ${secs}s" >&2; sleep "$secs" ;;
    reset_session)   step_reset_session ;;
    restart)         step_restart ;;
    resume)          step_resume ;;
    sigkill)         step_sigkill ;;
    exit_clean)      step_exit_clean ;;
    start_session)   step_start_session "$(jq -r '.cwd // empty' <<<"$step")" ;;
    session)         : ;;   # pure focus switch — handled by the inline target block
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; STEP_OK=false ;;
  esac
  (( $(date +%s) >= DEADLINE )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Persist the final active state.
save_active

# Best-effort: any slot that never resolved a transcript gets one last attempt.
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

# Staging contract: primary session.uuid is gemini's daemon-side session_id
# (chats filename stem) so run-cell's primary-skip comparison and curate's
# `.session_id` filter both match. emit_session_contract handles the combined
# stdout, exit-reason, and the multi-session lists (_lib/drive/contracts.sh).
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"

echo "drive-gemini-cli-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=$(daemon_sid "${SES_TRANSCRIPT[1]}"), transcript=${SES_TRANSCRIPT[1]})"

drive_exit
