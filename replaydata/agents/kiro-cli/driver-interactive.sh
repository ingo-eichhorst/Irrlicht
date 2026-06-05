#!/usr/bin/env bash
# drive-kiro-cli-interactive.sh — drive kiro-cli's TUI via tmux, executing a
# step-script. SCAFFOLDED from scripts/templates/drive-interactive.sh.tmpl
# (#496 RC2): a new adapter starts with EVERY standard step-type arm present
# (stubbed), not a 3-step stub — so the column driver-gap forecast tells you
# which primitives still need porting, and the matrix can't silently freeze a
# cell on a missing arm.
#
# Ported seams (this column's create-agent scaffold + the session-end record
# port): send / slash / wait_turn / sleep / keys work; exit_clean / sigkill /
# restart / resume are ported from drive-codex-interactive.sh (kiro mints its OWN
# session UUID per launch and is discovered via a MARKER, exactly like codex — so
# the codex slot model and these teardown/resume primitives transplant cleanly).
# resume relaunches `kiro-cli chat --trust-all-tools --resume-id <uuid>`, which
# re-opens the SAME <uuid>.jsonl and appends under a NEW PID (one session, two
# lifetimes — same as codex's `codex resume`). The remaining multi-session /
# signal primitives (interrupt, reset_session, start_session, session) stay
# stubbed — `record` ports each when a recipe first needs it.
#
# IMPORTANT — how a stubbed arm is caught: every standard arm is PRESENT here
# (so recipe-lint's GRAMMAR check treats it as handled and will NOT report a
# driver_gap for it). The real backstop is the SEMANTIC lint: the DRIVE_ELICITS
# constant below lists ONLY the step types this driver actually elicits, and
# recipe-lint reads it straight from this file (#508 #4 — no separate manifest)
# and flags any recipe needing a stubbed-but-unlisted primitive as a
# semantic_gap (exit 4) BEFORE recording. Keep DRIVE_ELICITS accurate: add a
# primitive the moment you genuinely port its seam, not when you stub the arm.
#
# Standard step types (port each from the reference driver):
#   send / slash   — type text + Enter (slash is the same keystrokes)
#   wait_turn      — block until the agent finishes the turn (SEAM 2)
#   interrupt      — cancel the in-flight turn (Escape / Ctrl-C)
#   keys           — raw tmux key sequence (arrow-key pickers, etc.)
#   sleep          — pause N seconds
#   reset_session  — in-REPL reset (/clear, /new): same process, new session id
#   restart        — end the session, start a FRESH one (new id, new cwd)
#   resume         — relaunch the SAME id+cwd (daemon sees one session, 2 PIDs)
#   sigkill        — kill -9 the active session's PID
#   exit_clean     — /quit graceful shutdown
#   start_session  — launch a concurrent session without tearing the first down
#   session        — switch the active slot (carried as {"session": N})
#
# ----------------------------------------------------------------------------
# NO HEADLESS PATH (deliberate)
#   kiro-cli DOES have a headless mode (`kiro-cli chat --no-interactive …`) but
#   it writes NO session file under ~/.kiro/sessions/cli/ — so headless runs are
#   invisible to the daemon and USELESS for recording (FINDINGS.md §7). Only the
#   TUI (`kiro-cli chat --trust-all-tools`) persists a transcript, so this
#   driver is TUI-only; there is no opencode-style headless escape hatch.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-kiro-cli-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-kiro-cli-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# $2 (preferred-uuid) is accepted for ABI parity with the other interactive
# drivers; kiro-cli assigns its own UUID per launch, so it is unused here.
TIMEOUT_S="$3"
# $4 (settings-path) carries the scenario's settings blob. kiro-cli reads no
# --settings flag, but one knob is honored: `trust_all_tools` (default true).
# When a cell sets it false, boot_session launches plain `kiro-cli chat` (no
# --trust-all-tools) so a mutating tool call raises the write-approval picker —
# the auto-classified-permission cell needs this to observe the permission gate.
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

# TRUST_ALL defaults to true (every other kiro cell relies on persisted
# trust-all consent so no per-tool picker stalls the run); a cell opts out with
# settings.trust_all_tools=false to make the approval picker appear.
TRUST_ALL=true
if [[ -f "$SETTINGS_PATH" ]] && [[ "$(jq -r '.trust_all_tools // "true"' "$SETTINGS_PATH" 2>/dev/null)" == "false" ]]; then
  TRUST_ALL=false
fi

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# kiro-cli mints its OWN session UUID per `chat` launch and writes
# ~/.kiro/sessions/cli/<uuid>.jsonl. We discover it as the newest .jsonl whose
# mtime is after the slot's MARKER; the session id is the filename stem (the
# daemon keys on it and it is the `--resume-id` arg).
KIRO_SESSIONS_DIR="$HOME/.kiro/sessions/cli"
mkdir -p "$KIRO_SESSIONS_DIR"

# Per-run CWD so each launch has its own cwd, isolating the cwd-based PID match.
# run-cell.sh's cross-adapter mode overrides this via $IRRLICHT_ONBOARD_CWD so a
# second, different adapter can share the SAME workspace.
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match

DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# Active-session view — the step functions read/write these. They are a cache of
# the active slot's state, kept in sync via save_active / load_slot. TRANSCRIPT
# is the absolute <uuid>.jsonl path; UUID is the bare session id (filename stem,
# the `--resume-id` arg); SESSION is the tmux session name; MARKER gates
# transcript discovery for this slot (resolve_transcript only considers session
# files NEWER than it).
SESSION=""
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
MARKER=""

# Per-slot state (1-based; index 0 unused). Each slot is one session lifetime.
# SES_ALIVE[i]=1 while its tmux session is still running.
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
# shared model in _lib/drive/slots.sh — codex/pi share it byte-for-byte; kiro
# discovers its session id exactly the same way (own UUID, MARKER-gated newest
# .jsonl), so it sources the same lib (#508 #3). The per-slot marker filename is
# set via DRIVE_MARKER_PREFIX.
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
DRIVE_MARKER_PREFIX="$STAGING/.kiro-launch-marker"
# shellcheck source=../../_lib/drive/slots.sh
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=../../_lib/drive/contracts.sh
source "$_DRIVE_LIB/contracts.sh"

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). A stubbed `not_implemented`
# arm must NOT be listed, so recipe-lint flags a recipe needing it as a
# semantic_gap before recording. kiro-cli is TUI-first: `kiro-cli chat
# --trust-all-tools` is the ONLY mode that persists a session file under
# ~/.kiro/sessions/cli/. Headless `--no-interactive` writes NO session file, so
# it is invisible to the daemon and MUST NOT be used for recording (FINDINGS.md
# §7). slash commands reach the live TUI directly (a bare send "/cmd" is NOT
# stored as literal text), so DRIVE_SLASH_REQUIRES_STEP_TYPE stays false.
DRIVE_ELICITS="send slash wait_turn sleep keys restart resume sigkill exit_clean"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for kiro-cli — see scripts/templates/drive-interactive.sh.tmpl and drive-codex-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear every still-alive tmux session
# down. Set EXIT_REASON before a failing `exit` so the reason is accurate.
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ "${SES_ALIVE[$i]:-0}" == "1" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# Launch `kiro-cli chat --trust-all-tools` (the TUI; --trust-all-tools assumes
# the persisted trust-all consent so no per-tool picker stalls the run) in the
# active slot's tmux session + cwd. The slot's MARKER (touched by alloc_slot)
# gates transcript discovery: kiro creates <uuid>.jsonl only after the first
# prompt, so resolve_transcript picks the newest session file with mtime >
# MARKER. Wait for the idle input line so keystrokes aren't swallowed during TUI
# boot.
boot_session() {
  # Optional $1 = a resume-id. When set, the launch becomes
  # `kiro-cli chat --trust-all-tools --resume-id <uuid>`, which re-opens the
  # SAME ~/.kiro/sessions/cli/<uuid>.jsonl and APPENDS to it under a NEW PID
  # (verified live) — that is the resume seam (step_resume). A bare boot
  # (restart / first launch) passes nothing and kiro mints a fresh UUID.
  local resume_id="${1:-}"
  local sess="$SESSION" cwd="${SES_CWD[$ACTIVE]}"
  local slot_stdout="$DRIVER_LOG.stdout.$ACTIVE"
  : > "$slot_stdout"
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  mkdir -p "$cwd"
  tmux kill-session -t "$sess" 2>/dev/null || true
  # The marker's mtime is the discovery floor; with 1s mtime granularity, sleep
  # 1 AFTER touching it (alloc_slot already touched it) so a session file
  # written in the same second still sorts after the floor.
  sleep 1
  local launch="kiro-cli chat"
  $TRUST_ALL && launch="$launch --trust-all-tools"
  [[ -n "$resume_id" ]] && launch="$launch --resume-id $resume_id"
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  tmux new-session -d -s "$sess" -x 200 -y 50 -c "$cwd" \
    "$launch" \
    >>"$DRIVER_LOG.stdout" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch kiro-cli under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$sess" -o "cat >> '$slot_stdout'"
  echo "[driver] tmux started: $sess (slot=$ACTIVE, cwd=$cwd)" >&2
  # Wait for the idle input line ("ask a question or describe a task") so the
  # TUI is ready to accept keystrokes before the first send.
  local waited=0
  while (( waited < 60 )); do
    if tmux capture-pane -t "$sess" -p -S -40 2>/dev/null | grep -q 'ask a question or describe a task'; then
      break
    fi
    sleep 0.5; waited=$((waited + 1))
  done
  sleep 1  # extra grace for the prompt to settle
}

# resolve_transcript binds TRANSCRIPT + UUID to this slot's session file: the
# newest ~/.kiro/sessions/cli/<uuid>.jsonl with mtime newer than this slot's
# MARKER. kiro writes it lazily (only after the first prompt), so this is called
# from wait_turn (after a send) and polls briefly. The session id is the
# filename stem — the daemon keys on it and it is the `--resume-id` arg.
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  local f line
  for _ in $(seq 1 60); do
    # newest .jsonl with mtime > this slot's MARKER. `find -newer` filters by the
    # floor; the per-line loop chooses the newest (avoids xargs -r, unportable on
    # BSD, and the empty-input → list-cwd footgun of a bare `ls -t`).
    f=""
    while IFS= read -r line; do
      [[ -z "$line" ]] && continue
      transcript_claimed "$line" && continue
      if [[ -z "$f" || "$line" -nt "$f" ]]; then f="$line"; fi
    done < <(find "$KIRO_SESSIONS_DIR" -maxdepth 1 -type f -name '*.jsonl' \
                  -newer "$MARKER" 2>/dev/null)
    if [[ -n "$f" && -s "$f" ]]; then
      TRANSCRIPT="$f"
      UUID="$(basename "$f" .jsonl)"
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# transcript_claimed reports whether an absolute transcript path is already bound
# to a DIFFERENT slot, so a later slot's discovery never re-binds an earlier
# slot's <uuid>.jsonl when per-slot markers collide at 1s mtime granularity.
transcript_claimed() {
  local p="$1" i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ $i -eq $ACTIVE ]] && continue
    [[ "${SES_TRANSCRIPT[$i]}" == "$p" ]] && return 0
  done
  return 1
}

# turn_count counts COMPLETED turns: AssistantMessage lines whose data.content[]
# has NO toolUse block (text-only). Mirrors kirocli/parser.go — a text-only
# AssistantMessage is turn_done; mid-turn AssistantMessages carry toolUse blocks
# and are NOT counted. (FINDINGS.md §5.)
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.kind=="AssistantMessage")
           | select([.data.content[]? | select(.kind=="toolUse")] | length == 0)
           | "x"' "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Transcript-based: block until turn_count reaches EXPECTED_TURNS (one text-only
# AssistantMessage per completed user turn). Returns 0 on a new completed turn,
# 1 on readiness/turn timeout.
wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: kiro never created a session file under $KIRO_SESSIONS_DIR" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while (( $(remaining_seconds) > 0 )); do
    now=$(turn_count)
    if (( now >= EXPECTED_TURNS )); then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected >= $EXPECTED_TURNS)" >&2
      return 0
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected >= $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# Type text + Enter into the live TUI. A brief pause lets the input handler
# render the typed text before Enter lands (otherwise Enter can race the render
# and be dropped). Each send bumps EXPECTED_TURNS so wait_turn knows how many
# text-only AssistantMessages to expect.
send_text() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${1:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

# A synchronous (no-LLM) slash command — /help, /session-id — renders in the TUI
# but appends NO text-only AssistantMessage to the .jsonl, so the transcript-based
# turn_count never increments for it. Deliver the keystrokes exactly like a send,
# but do NOT bump EXPECTED_TURNS (otherwise a later wait_turn waits for a turn that
# can never materialize and times out). See synchronous-slash-command recipe.
#
# kiro-cli 2.5.x: some slashes (/help) open a MODAL overlay ("ESC to close") that
# captures all subsequent input until dismissed — a following send would vanish
# into the overlay's scroll handler. Send a trailing ESC to dismiss any overlay
# so the next prompt reaches the REPL. ESC on a non-overlay slash (/session-id)
# is a harmless no-op (verified live: the prompt stays usable).
send_slash() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  sleep 1.5
  tmux send-keys -t "$SESSION" Escape
  echo "[driver] slash[s$ACTIVE]: ${1:0:60} (no turn expected, overlay dismissed)" >&2
}

# A raw tmux key sequence (NOT literal text): each space-separated token is one
# tmux key event (`!`, `e`, `Space`, `Enter`, `Up`, `Escape`, …), so no `-l`
# flag and NO implicit Enter is appended — the recipe spells out its own Enter.
# Used to drive a `!command` shell escape (`! e c h o Space h e l l o Enter`):
# kiro runs it as a LOCAL shell command with no LLM round-trip, so it appends no
# text-only AssistantMessage and the transcript turn_count never increments.
# Therefore keys does NOT bump EXPECTED_TURNS (a later wait_turn must not wait on
# a turn the escape can never produce — same reasoning as send_slash). Mirrors
# step_keys in drive-claudecode-interactive.sh.
send_keys() { # <key-token-list>
  # shellcheck disable=SC2086 — intentional word-splitting of the key list
  tmux send-keys -t "$SESSION" $1
  echo "[driver] keys[s$ACTIVE]: $1 (no turn expected)" >&2
  sleep 0.3
}

# --- TEARDOWN SEAM A: exit_clean ---------------------------------------------
# kiro-cli's `/quit` (alias `/exit`) ends the chat cleanly: kiro prints
# "Session ended.", removes the <uuid>.lock live-session marker, and the
# `kiro-cli` parent process exits, so the daemon's process scanner stops
# matching it and emits process_exited (FINDINGS.md §8; LIVE-verified: /quit
# removed the session row). Send /quit exactly like a slash command (text +
# Enter), then give kiro time to flush its final transcript lines and tear down
# before the next step. Unlike claudecode/codex (Ctrl-D), kiro binds the exit to
# the /quit command, not to Ctrl-D — though Ctrl-D also works.
step_exit_clean() {
  resolve_transcript || true
  tmux send-keys -t "$SESSION" -l -- "/quit"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  sleep 2
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean[s$ACTIVE]: sent /quit to $SESSION (uuid=$UUID)" >&2
}

# --- TEARDOWN SEAM B: sigkill ------------------------------------------------
# kill -9 the active slot's kiro-cli parent — abrupt teardown with no flush (the
# SIGKILL counterpart to exit_clean's graceful /quit). Target exactly the
# process the daemon tracks: the daemon discovers the PID by matching a
# `kiro-cli` process whose OS cwd equals the session cwd (kirocli/pid.go →
# DiscoverPIDByCWD; kiro does NOT hold the .jsonl open, so transcript-writer
# discovery is impossible). Mirror that lookup here so the SIGKILL lands on the
# daemon's PID and process_exited fires.
#
# SAFETY: `pgrep -x kiro-cli` exact-matches ONLY the bare `kiro-cli` parent — it
# never matches the always-running `kiro_cli_desktop` companion (different comm).
# We then narrow to THIS slot's cwd via lsof, so a concurrent kiro-cli in a
# different cwd (another recording, a user REPL) is never touched.
step_sigkill() {
  # NB: separate `local` statements — `local a=… b=$a` reads $a before it is set
  # under `set -u` (bash evaluates the RHS of a same-line second var against the
  # PRIOR scope), which trips "unbound variable".
  local slot_cwd="${SES_CWD[$ACTIVE]}"
  local resolved="$slot_cwd"
  # Canonicalize the cwd the way the daemon does (it EvalSymlinks before the
  # equality check), so a symlinked $HOME still matches lsof's resolved cwd.
  if command -v python3 >/dev/null 2>&1; then
    resolved="$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$slot_cwd" 2>/dev/null || echo "$slot_cwd")"
  fi
  local pid="" cand cwd
  for cand in $(pgrep -x kiro-cli 2>/dev/null); do
    # lsof's cwd (FD "cwd") is the OS-canonical working directory — compare it to
    # both the raw and the realpath-resolved slot cwd.
    cwd="$(lsof -a -p "$cand" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
    if [[ -n "$cwd" && ( "$cwd" == "$slot_cwd" || "$cwd" == "$resolved" ) ]]; then
      pid="$cand"
      break
    fi
  done
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (uuid=$UUID, cwd=$slot_cwd)" >&2
  else
    echo "[driver] sigkill[s$ACTIVE]: no kiro-cli PID found for cwd=$slot_cwd (uuid=$UUID)" >&2
  fi
  SES_ALIVE[$ACTIVE]=0
  # Leave the dead tmux pane for teardown — the kill alone produces
  # process_exited.
  sleep 1
}

# --- TEARDOWN SEAM C: restart ------------------------------------------------
# End the active session and start a FRESH kiro-cli (new launch → new UUID, new
# cwd). Mirrors drive-codex-interactive.sh's restart: used between session-end
# variants so each lands as its own session row, separated by a grey gap where
# no session is alive between variants. By the time restart runs the active
# process is usually already gone (an exit_clean or sigkill preceded it); retire
# the slot regardless but keep it in the list so the epilogue flushes its
# session id. A fresh cwd gives each variant an unambiguous cwd-based PID match
# (kiro-cli launches with persisted --trust-all-tools consent, so a fresh cwd
# never stalls on a trust picker — caveat in the cell spec).
step_restart() {
  resolve_transcript || true
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "kiro-clidrv-$$-$(date +%s)-${idx}" "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${RUN_CWD}-${idx})" >&2
  boot_session
}

# --- TEARDOWN SEAM D: resume -------------------------------------------------
# Relaunch the SAME session id in the SAME cwd under a NEW process lifetime.
# Mirrors step_resume in drive-codex-interactive.sh: kiro appends to the SAME
# ~/.kiro/sessions/cli/<uuid>.jsonl across both lifetimes (verified live:
# `kiro-cli chat --trust-all-tools --resume-id <uuid>` re-opens the same .jsonl,
# new PID), so this is ONE session (one slot) with two process lifetimes —
# TRANSCRIPT/UUID/MARKER stay unchanged and we do NOT allocate a new slot (that
# would double-list the session and double-concat the transcript at curate
# time). Only the tmux session name rotates.
#
# The active process is usually already gone here (an exit_clean preceded it,
# and the recipe sleeps >10s so the daemon's deletedCooldown elapses before the
# same UUID re-creates the row). If it is somehow still alive, end it cleanly
# first so the resumed PID is the only kiro-cli holding this UUID's cwd.
step_resume() {
  resolve_transcript || true
  local resume_uuid="$UUID"
  local saved_transcript="$TRANSCRIPT"

  if [[ "${SES_ALIVE[$ACTIVE]:-0}" == "1" ]]; then
    tmux send-keys -t "$SESSION" -l -- "/quit"
    sleep 0.3
    tmux send-keys -t "$SESSION" Enter
    sleep 2
  fi
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1

  # Rotate ONLY the tmux session name; keep the slot's cwd/uuid/transcript so
  # the relaunch resumes the same id and resolve_transcript is not re-run
  # (re-finding could race the new PID before it re-opens the .jsonl).
  SESSION="kiro-clidrv-$$-$(date +%s)-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"
  SES_ALIVE[$ACTIVE]=1

  if [[ -n "$resume_uuid" ]]; then
    echo "[driver] resume[s$ACTIVE]: relaunch kiro-cli --resume-id $resume_uuid (same .jsonl=$saved_transcript)" >&2
    boot_session "$resume_uuid"
  else
    echo "[driver] resume[s$ACTIVE]: UUID unknown — cannot resume; aborting" >&2
    EXIT_REASON="nonzero(3)"
    SES_ALIVE[$ACTIVE]=0
    return 3
  fi
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
# Bring up the first session as slot 1. SCRIPT_JSON's restart steps allocate
# further slots.
alloc_slot "kiro-clidrv-$$-$(date +%s)" "$RUN_CWD"
boot_session

STEP_OK=true
while IFS= read -r step; do
  $STEP_OK || break
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    send)            send_text "$(jq -r '.text' <<<"$step")" ;;
    slash)           send_slash "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || STEP_OK=false ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       not_implemented interrupt || STEP_OK=false ;;     # TODO(kiro-cli): Escape/Ctrl-C the in-flight turn
    keys)            send_keys "$(jq -r '.keys' <<<"$step")" ;;
    reset_session)   not_implemented reset_session || STEP_OK=false ;; # TODO(kiro-cli): in-REPL /clear|/new → new session id
    restart)         step_restart ;;
    resume)          step_resume || STEP_OK=false ;;
    sigkill)         step_sigkill ;;
    exit_clean)      step_exit_clean ;;
    start_session)   not_implemented start_session || STEP_OK=false ;; # TODO(kiro-cli): concurrent session, keep first alive
    session)         not_implemented session || STEP_OK=false ;;       # TODO(kiro-cli): switch active slot
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; STEP_OK=false ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; STEP_OK=false; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Persist the final active state.
save_active

# Best-effort: any slot that never resolved a transcript (e.g. a variant whose
# process died before its first wait_turn) gets one last resolution attempt.
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

# Combined stdout log for backward-compat.
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

# --- Write the staging contract (shared epilogue) ----------------------------
# emit_session_contract writes driver.exit-reason, session.uuid + transcript.path
# (slot 1), and the multi-session session.uuids / transcript.paths lists. The
# primary session.uuid is daemon_sid(slot-1 transcript) — for kiro that stem IS
# the UUID. cleanup() also writes driver.exit-reason; this overwrites it with the
# same value, which is fine.
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"

echo "drive-kiro-cli-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=$(daemon_sid "${SES_TRANSCRIPT[1]}"), transcript=${SES_TRANSCRIPT[1]})"

drive_exit
