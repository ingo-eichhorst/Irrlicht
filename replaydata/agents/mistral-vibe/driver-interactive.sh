#!/usr/bin/env bash
# drive-mistral-vibe-interactive.sh — drive mistral-vibe's REPL via tmux, executing a
# step-script. SCAFFOLDED from scripts/templates/drive-interactive.sh.tmpl
# (#496 RC2): a new adapter starts with EVERY standard step-type arm present
# (stubbed), not a 3-step stub — so the column driver-gap forecast tells you
# which primitives still need porting, and the matrix can't silently freeze a
# cell on a missing arm.
#
# It also starts ON the shared _lib/drive slot model (slots.sh + contracts.sh):
# the multi-session bookkeeping and the staging-contract emission are already
# wired, so porting a multi-session step is "fill the seam + call
# alloc_slot/load_slot", never a mid-run refactor onto the slot model (#666).
#
# HOW TO USE THIS TEMPLATE
#   cp scripts/templates/drive-interactive.sh.tmpl \
#      scripts/drive-<agent>-interactive.sh
#   sed -i '' 's/mistral-vibe/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(mistral-vibe) below by
# porting from the reference drivers. For the slot-model multi-session arms
# (restart / resume / reset_session / start_session / session) read a slot-model
# driver — drive-codex-interactive.sh or drive-gemini-cli-interactive.sh;
# claudecode uses a different slot scheme and does NOT source _lib/drive.
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
#   exit_clean     — Ctrl-D graceful shutdown
#   start_session  — launch a concurrent session without tearing the first down
#   session        — switch the active slot (carried as {"session": N})
#
# ----------------------------------------------------------------------------
# HEADLESS ESCAPE HATCH
#   If mistral-vibe has a true headless-per-turn mode (e.g. `mistral-vibe run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if mistral-vibe is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-mistral-vibe-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-mistral-vibe-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
UUID="$2"            # preferred session id; some agents mint their own (ignore then)
TIMEOUT_S="$3"
SETTINGS_PATH="$4"   # scenario settings blob; wire into the launch if the agent reads one
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"

# Shared multi-session slot bookkeeping + staging-contract emission (#508 #3).
# The scaffolded driver lives at replaydata/agents/<agent>/driver-interactive.sh,
# so the lib is two dirs up under replaydata/_lib/drive. Sourcing it means a new
# column starts ON the slot model: porting a multi-session step is "wire the seam
# + call alloc_slot/load_slot", not rebuilding the slot bookkeeping (#666).
_DRIVE_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../_lib/drive" && pwd)"
DRIVE_MARKER_PREFIX="$STAGING/.mistral-vibe-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/dialogs.sh"
source "$_DRIVE_LIB/teardown.sh"

# Slot state the lib reads/writes (the driver owns these globals). A run starts
# with zero slots; launch_repl allocs slot 1, and restart/start_session alloc
# more. ACTIVE indexes the live slot; SESSION/TRANSCRIPT/UUID mirror it.
N_SLOTS=0; ACTIVE=0
SES_SESSION=(); SES_TRANSCRIPT=(); SES_UUID=(); SES_EXPECTED=()
SES_MARKER=(); SES_CWD=(); SES_ALIVE=()

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Start with ONLY the seams
# that actually work in this scaffold (send/slash/sleep) and add each primitive
# as you port its seam — a stubbed `not_implemented` arm must NOT be listed, so
# recipe-lint flags a recipe needing it as a semantic_gap before recording. Set
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if mistral-vibe is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
# Tool-executing recipes: set the recipe's settings.bypass_tool_permissions=true
# and launch_repl adds `vibe --auto-approve` so tool calls run unattended (not a
# step type — a launch-mode toggle, so it stays out of DRIVE_ELICITS).
DRIVE_ELICITS="send slash sleep wait_turn exit_clean restart sigkill resume keys start_session session interrupt"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
SESSION=""
# Session dirs that already existed when this run launched. Vibe creates a new
# ~/.vibe/logs/session/<session-id>/ LAZILY on the first user message, so the
# fresh dir is resolved by set-difference against this snapshot (SEAM 3), not
# by "newest mtime" — an older session's mtime can be bumped and win that race.
PRE_LAUNCH_DIRS=""

# daemon_sid OVERRIDE (vibe-specific): slots.sh's default maps a transcript
# path to its filename stem, but every Vibe session writes the SAME constant
# basename `messages.jsonl` — the daemon keys the session on the PARENT
# directory name instead (see vibe/adapter.go sessionIDFromPath). So the
# daemon's session_id is the <session-id> dir one level above the file, and
# both emit_session_contract's session.uuid and the multi-session session.uuids
# list (contracts.sh) must carry that form so curation's `.session_id` filter
# matches the daemon's events.jsonl. Defined AFTER `source slots.sh` so it wins.
daemon_sid() {
  local p="$1"
  [[ -z "$p" ]] && { echo ""; return; }
  basename "$(dirname "$p")"
}

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

# resolve_transcript binds the active slot to the session dir Vibe minted for
# this run. It is LAZY: the dir appears only after the first user message, so
# this is called from wait_turn, not launch. Pick the session_* dir that did
# NOT exist pre-launch and now holds a non-empty messages.jsonl; cache it on
# the active slot (TRANSCRIPT is the messages.jsonl, UUID is the dir name).
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  local sroot="$HOME/.vibe/logs/session"
  local _ d cand
  for _ in $(seq 1 60); do
    cand=""
    while IFS= read -r d; do
      [[ -z "$d" ]] && continue
      grep -qxF "$d" <<<"$PRE_LAUNCH_DIRS" && continue
      [[ -f "$d/messages.jsonl" && -s "$d/messages.jsonl" ]] || continue
      cand="$d"
    done < <(find "$sroot" -maxdepth 1 -type d -name 'session_*' 2>/dev/null | sort)
    if [[ -n "$cand" ]]; then
      TRANSCRIPT="$cand/messages.jsonl"
      UUID="$(basename "$cand")"
      SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
      SES_UUID[$ACTIVE]="$UUID"
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (sid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# turn_count counts COMPLETED assistant turns in the transcript. Mirrors the
# vibe adapter's turn_done classification: a role:"assistant" line WITH content
# and NO tool_calls is a finished turn. An assistant line carrying tool_calls
# (still working) and role:"tool" lines are intentionally excluded, so a
# tool-using turn only counts once its final answer lands.
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.role=="assistant" and ((.content // "") != "") and (((.tool_calls // []) | length) == 0)) | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for mistral-vibe — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down if a session was
# started. Set EXIT_REASON before a failing `exit` so the reason is accurate.
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
trap cleanup EXIT

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# Port from the reference driver. Start the agent in a detached tmux session in
# $RUN_CWD, capturing stdout/stderr to "$DRIVER_LOG.stdout|.stderr". Pass the
# preferred UUID if the agent accepts one. The cleanup trap above tears it down.
# If the FIRST recipe step is a send/slash, its text is delivered as vibe's
# positional PROMPT arg at launch instead of typed into the TUI. Vibe's Textual
# welcome-banner animation + model-list init can outlast any capture-pane
# readiness heuristic, and a keystroke typed before the input box is live is
# silently dropped (no session dir is ever created). Launching `vibe "<prompt>"`
# sidesteps TUI-input timing entirely: vibe boots, submits the prompt, and stays
# interactive. Empty when the first step isn't a send (then we fall back to the
# tmux-send path with a readiness wait).
# Only a plain `send` first step becomes vibe's launch positional prompt. Two
# kinds of first step must instead be typed into the live REPL (bare-launch path):
#   - a slash COMMAND (e.g. /loop): as the positional, vibe runs it once as a
#     plain user message instead of registering it (a scheduled loop never starts);
#   - a `!`-prefixed SHELL ESCAPE (e.g. !echo hello): as the positional, vibe
#     sends it to the LLM (which runs it as a bash TOOL call — a full working
#     turn) instead of intercepting the `!` in the REPL as a direct shell escape.
FIRST_PROMPT="$(jq -r '.[0] | select(.type=="send" and ((.text // "") | startswith("!") | not)) | .text // empty' <<<"$SCRIPT_JSON" 2>/dev/null || true)"
FIRST_SEND_PENDING=0

# boot_vibe_active <positional-prompt> <resume-id>
# Launch vibe in the ALREADY-ALLOCATED active slot's tmux session + cwd. Shared
# by the initial launch (launch_repl), restart (fresh session), and resume
# (relaunch the same session id). Both args may be empty:
#   <positional-prompt> — delivered as vibe's launch PROMPT arg so vibe boots +
#                         submits it without any TUI keystroke timing; empty ⇒
#                         bare launch and we wait for REPL readiness so a later
#                         send types into a live input box.
#   <resume-id>         — adds `--resume <id>` so vibe reopens the SAME
#                         session_<ts>_<shortid> dir and appends (one session,
#                         two process lifetimes). Empty ⇒ fresh session.
# Snapshots PRE_LAUNCH_DIRS so resolve_transcript's set-diff picks the dir vibe
# mints for THIS launch (lazy — created on the first user message), never an
# older bumped session.
boot_vibe_active() {
  local prompt="$1" resume_id="$2"
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  PRE_LAUNCH_DIRS="$(find "$HOME/.vibe/logs/session" -maxdepth 1 -type d -name 'session_*' 2>/dev/null | sort)"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  : > "$DRIVER_LOG.stdout.$ACTIVE"
  mkdir -p "${SES_CWD[$ACTIVE]}"
  # The binary is `vibe` (the console-script), NOT `mistral-vibe`; the OS process
  # is a python interpreter running .../bin/vibe. Vibe is an interactive Textual
  # TUI, so drive it under tmux. `|| { … exit … }` keeps a launch failure from
  # aborting under set -e WITHOUT an accurate exit-reason — the cleanup trap then
  # records nonzero(2).
  # --trust: auto-trust the workdir. The onboarding staging cwd lives UNDER the
  # irrlicht git repo, so vibe otherwise fires a blocking "Trust folder or
  # repository?" dialog (it detects the repo's AGENTS.md) that stalls init and
  # prevents the session dir from ever being created. --trust makes launch
  # deterministic regardless of where the staging cwd lands.
  local -a vibe_args=(--trust)
  # --auto-approve (a.k.a. --yolo): approve ALL tool calls without prompting, so
  # a recipe whose turn invokes a tool (bash/read/write) runs unattended and
  # wait_turn sees the terminal assistant line instead of stalling on a blocking
  # permission dialog. Gated on the recipe's settings blob (bypass_tool_permissions
  # == true) — NOT unconditional, so a future vibe tool-gate/permission-prompt cell
  # can still observe the working→waiting arc. run-cell.sh writes .settings to
  # $SETTINGS_PATH; a missing/`{}`/false setting leaves tool calls gated (default).
  if [[ -f "$SETTINGS_PATH" ]] && jq -e '.bypass_tool_permissions == true' "$SETTINGS_PATH" >/dev/null 2>&1; then
    vibe_args+=(--auto-approve)
    echo "[driver] bypass_tool_permissions: launching vibe with --auto-approve" >&2
  fi
  if [[ -n "$resume_id" ]]; then
    vibe_args+=(--resume "$resume_id")
    echo "[driver] resume: launching vibe --resume $resume_id" >&2
  fi
  if [[ -n "$prompt" ]]; then
    vibe_args+=("$prompt")
    echo "[driver] launching with prompt as positional arg: ${prompt:0:60}" >&2
  fi
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" -- \
    vibe "${vibe_args[@]}" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch vibe under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout.$ACTIVE'"
  echo "[driver] tmux started: $SESSION (slot=$ACTIVE, cwd=${SES_CWD[$ACTIVE]})" >&2
  # Bare-launch fallback (no positional prompt): a later send types into the
  # TUI, so wait for the banner to render and the "Initializing…" spinner to
  # clear before the first keystroke lands. Skipped on the positional path —
  # vibe processes the launch prompt itself, so no keystroke timing to gate.
  if [[ -z "$prompt" ]]; then
    local WAITED=0
    while [[ $WAITED -lt 120 ]]; do
      tmux capture-pane -t "$SESSION" -p -S -40 2>/dev/null | grep -q 'Type /help' && break
      sleep 0.5; WAITED=$((WAITED + 1))
    done
    WAITED=0
    while [[ $WAITED -lt 120 ]]; do
      tmux capture-pane -t "$SESSION" -p -S -40 2>/dev/null | grep -q 'Initializing' || break
      sleep 0.5; WAITED=$((WAITED + 1))
    done
    sleep 2  # extra grace for the input prompt to settle
  fi
  # Transcript is resolved lazily in wait_turn — Vibe's session dir does not
  # exist until the first user message is processed.
}

launch_repl() {
  # alloc_slot mints slot 1, points SESSION at its tmux name and ACTIVE at it,
  # and clears the slot's TRANSCRIPT/UUID. restart calls alloc_slot again for a
  # fresh session; per-slot stdout (.stdout.$ACTIVE) feeds the contract.
  alloc_slot "mistral-vibedrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
  [[ -n "$FIRST_PROMPT" ]] && FIRST_SEND_PENDING=1
  boot_vibe_active "$FIRST_PROMPT" ""
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Port the agent's turn-done signal: claudecode polls the transcript for
# stop_reason=="end_turn"; codex polls the rollout for task_complete; opencode
# polls the SQLite store for a step-finish. Return 0 when a NEW turn completed
# (or times out via remaining_seconds()).
#
# A tool call that resolves to ToolPermission.ASK holds the turn on a TUI-only
# approval_app dialog ("Permission for the <tool> tool" / "Allow for remainder
# of this session") that never touches messages.jsonl (see sibling
# 2-19_tool-gate-permission-prompt) — turn_count alone would stall on that
# until DEADLINE. Poll the live pane on every tick and dismiss the dialog the
# instant it renders, instead of guessing a fixed sleep window that's
# sometimes too short (dialog still rendering — recording fails as
# transcript_missing/timeout) and sometimes irrelevant (the turn already
# resolved, so a blind wait only delays an already-done recording) — issue
# #1003. This mirrors antigravity/driver-interactive.sh's wait_turn(), which
# already merges an identical mid-turn dialog dismiss into its own poll loop —
# both now call the shared _lib/drive/dialogs.sh helper (#1009) so the
# poll+dismiss mechanics live in one place; only the marker regex below stays
# adapter-local. The marker text matches the daemon's own trustDialogMarkers
# (core/domain/backchannel/uidetect.go) so the driver and the daemon agree on
# what "the dialog is up" means.
wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: vibe never created a session dir under ~/.vibe/logs/session" >&2
    EXIT_REASON="readiness_timeout"
    return 1
  }
  local now=0
  while [[ $(date +%s) -lt $DEADLINE ]]; do
    now=$(turn_count)
    if [[ $now -ge $EXPECTED_TURNS ]]; then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected >= $EXPECTED_TURNS)" >&2
      return 0
    fi
    if dismiss_dialog_if_visible "$SESSION" 'Permission for the|Allow for remainder of this session'; then
      echo "[driver] wait_turn[s$ACTIVE]: dismissed tool-permission dialog" >&2
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$now, expected >= $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"
  return 1
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# type_enter types text into the live REPL and submits it. The brief pause lets
# the Textual TUI's input handler render the typed text before Enter lands, so
# Enter isn't dropped mid-render. No turn accounting — callers own that.
type_enter() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
}

send_text() { # <text>
  # A leading '!' is a vibe SHELL ESCAPE: the REPL runs it directly in a subshell
  # and writes a !-result line that the daemon parser skips (#5e0b4f5c) — it is
  # NOT an LLM turn, so type it but do NOT bump EXPECTED_TURNS (else wait_turn
  # would wait for a turn that never comes). Never the launch positional (excluded
  # from FIRST_PROMPT above), so FIRST_SEND_PENDING is irrelevant here.
  if [[ "$1" == "!"* ]]; then
    type_enter "$1"
    echo "[driver] send[s$ACTIVE]: shell-escape ${1:0:60} (no turn expected)" >&2
    return 0
  fi
  # The first send/slash of a run is already delivered as the launch positional
  # prompt (see launch_repl) — just account for the turn, don't re-type it into
  # the TUI. Every later send types into the now-live REPL.
  if [[ "$FIRST_SEND_PENDING" == "1" ]]; then
    FIRST_SEND_PENDING=0
    EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
    echo "[driver] send[s$ACTIVE]: (delivered at launch) ${1:0:60} (expecting turn $EXPECTED_TURNS)" >&2
    return 0
  fi
  type_enter "$1"
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${1:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

# A session-rotating slash (/clear, /compact, /new) makes vibe mint a NEW
# ~/.vibe/logs/session/<id>/ dir under the SAME process + cwd (agent_loop.py
# _reset_session → session_logger.reset_session recomputes save_folder to a fresh
# session_<ts>_<hash> dir). The daemon surfaces that as a new session_id plus a
# transcript_removed on the prior same-PID session (#169 cleanup). A plain send
# would keep counting turns on the stale dir and time out, so after typing the
# command we re-arm SEAM-3 transcript resolution: snapshot the CURRENT dirs as
# pre-existing BEFORE typing (the new dir is created lazily on the next user
# message, so the set-diff in resolve_transcript then picks exactly it), clear the
# active slot's cached transcript, and reset the turn baseline (the new dir starts
# empty — the following send's answer is turn 1 in it).
ROTATING_SLASHES="/clear /compact /new"
# /rewind is a FORK slash: it opens the Textual RewindApp picker; selecting a
# checkpoint (driven by later `keys` steps) makes vibe's rewind_manager truncate
# the conversation and FORK to a NEW session_<ts>_<hash> dir (core/rewind manager
# → session_logger.reset_session), in the SAME process + cwd. The re-arm is
# identical to a rotating slash (snapshot dirs, clear the cached transcript,
# reset the turn baseline) — the only difference is the new dir materializes
# after the keys-driven selection + the next turn, not immediately, and the
# ORIGINAL session dir persists (rewind is a fork, not an in-place reset). The
# set-diff in resolve_transcript then binds the forked dir on the next wait_turn.
FORK_SLASHES="/rewind"
slash_cmd() { # <text>
  local text="$1"
  # A rotating slash can't be the launch positional (vibe needs a real prompt to
  # boot a session first); a non-rotating first slash just delegates to send.
  if [[ "$FIRST_SEND_PENDING" == "1" ]]; then
    send_text "$text"; return
  fi
  if [[ " $FORK_SLASHES " == *" $text "* ]]; then
    # FORK (/rewind): vibe forks to a NEW session dir in the SAME process, and
    # the ORIGINAL session PERSISTS (verify wants both rows). So — unlike a
    # rotating slash, which abandons the old session in place — bind + freeze the
    # original into its slot and ALLOCATE A NEW SLOT (reusing the same tmux name +
    # cwd, so the picker keys still land on the live process) for the forked
    # session. emit_session_contract then lists BOTH session ids so curation
    # unions the original's turns and the post-rewind fork.
    resolve_transcript || true
    local old_tmux="$SESSION" old_cwd="${SES_CWD[$ACTIVE]}"
    save_active
    SES_ALIVE[$ACTIVE]=0
    PRE_LAUNCH_DIRS="$(find "$HOME/.vibe/logs/session" -maxdepth 1 -type d -name 'session_*' 2>/dev/null | sort)"
    type_enter "$text"
    alloc_slot "$old_tmux" "$old_cwd"
    SES_ALIVE[$ACTIVE]=1
    echo "[driver] slash[s$ACTIVE]: $text → FORK; original frozen (sid=$(daemon_sid "${SES_TRANSCRIPT[$((ACTIVE-1))]}")), new slot awaits forked dir" >&2
    return 0
  fi
  if [[ " $ROTATING_SLASHES " == *" $text "* ]]; then
    PRE_LAUNCH_DIRS="$(find "$HOME/.vibe/logs/session" -maxdepth 1 -type d -name 'session_*' 2>/dev/null | sort)"
    type_enter "$text"
    TRANSCRIPT=""; UUID=""
    SES_TRANSCRIPT[$ACTIVE]=""; SES_UUID[$ACTIVE]=""
    EXPECTED_TURNS=0
    echo "[driver] slash[s$ACTIVE]: $text → session rotation; re-resolving transcript on next wait_turn" >&2
    return 0
  fi
  # Non-rotating slash (e.g. /help): types into the REPL but yields no assistant
  # turn, so don't bump EXPECTED_TURNS.
  type_enter "$text"
  echo "[driver] slash[s$ACTIVE]: $text (no turn expected)" >&2
}

# --- SEAM: interrupt (backchannel Ctrl-C) ------------------------------------
# step_interrupt — cancel the in-flight vibe turn. Vibe declares
# Interrupt: InterruptCtrlC (core/adapters/inbound/agents/vibe/agent.go), so the
# daemon's Controller aborts a running turn by delivering an ETX (Ctrl-C) into
# the terminal backend; the driver mirrors that exact keystroke to reproduce the
# backchannel interrupt. A cancelled turn never lands a completed assistant line
# in messages.jsonl (turn_count only counts role:"assistant" WITH content and NO
# tool_calls), so decrement EXPECTED_TURNS for the send whose turn we just
# aborted — mirroring codex/claudecode so a later wait_turn doesn't block on a
# turn that was interrupted away.
step_interrupt() {
  tmux send-keys -t "$SESSION" C-c
  if [[ $EXPECTED_TURNS -gt 0 ]]; then
    EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
  fi
  echo "[driver] interrupt[s$ACTIVE] (Ctrl-C, expecting turn $EXPECTED_TURNS)" >&2
  sleep 1
}

# --- SEAM: teardown primitives (exit_clean / restart / sigkill) --------------
# step_exit_clean — graceful shutdown. Vibe's `/exit` slash (handler _exit_app)
# quits directly with NO confirmation dialog (unlike Ctrl+D, which needs a
# second press within ~1s). Type it, then wait for the tmux session's process to
# die so the daemon observes process_exited before the next step runs.
step_exit_clean() {
  resolve_transcript || true
  type_enter "/exit"
  wait_tmux_session_gone "$SESSION" 15
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean[s$ACTIVE]: sent /exit; process exited (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
}

# step_restart — end the active session and start a FRESH vibe (new session dir,
# new session_id, fresh cwd). Separates session-end variants so each lands as
# its own session row with a grey gap between. By the time restart runs the
# active process is usually already gone (an exit_clean or sigkill preceded it);
# retire the slot regardless but keep it in the list so the epilogue flushes its
# session id. A fresh cwd keeps each variant's session cleanly separated.
step_restart() {
  resolve_transcript || true
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "mistral-vibedrv-$$-$(date +%s)-${idx}" "${RUN_CWD}-${idx}"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${SES_CWD[$ACTIVE]})" >&2
  boot_vibe_active "" ""
}

# step_sigkill — kill -9 the active slot's vibe process: abrupt teardown with no
# session flush (the SIGKILL counterpart to exit_clean's graceful /exit). The
# daemon binds the vibe PID by cwd+cmdline (vibe/pid.go DiscoverPIDByCWDAndCmdLine
# against `(^|/)vibe( |$)|mistral-vibe/bin/python`); tmux launches `vibe` as the
# pane's own process (`-- vibe …`, no shell wrapper), so the pane pid IS the
# daemon's tracked PID. kill it directly so process_exited fires.
step_sigkill() {
  resolve_transcript || true
  local pid
  pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
    wait_pid_gone "$pid" 1
  else
    echo "[driver] sigkill[s$ACTIVE]: no vibe PID found (session=$SESSION)" >&2
    sleep 1
  fi
  SES_ALIVE[$ACTIVE]=0
}

# step_resume — relaunch the SAME session in a new process lifetime. The
# preceding exit_clean already tore down the first lifetime; resume relaunches
# `vibe --resume <session-id>` in the SAME slot + cwd. Vibe reopens the ORIGINAL
# session_<ts>_<shortid> dir (cli.py load_session → resume_existing_session) and
# APPENDS to its messages.jsonl, so the daemon sees ONE session_id across both
# lifetimes — not a new session. The resume arg is the meta.json session_id
# (find_session_by_id shortens it to the 8-char dir suffix and globs); the dir
# name's trailing chunk is that same short id, used as a fallback. Keep the slot's
# TRANSCRIPT/UUID/EXPECTED_TURNS so the next send counts turn 2 in the SAME
# transcript (resolve_transcript is a no-op with TRANSCRIPT still cached) — no
# new slot is allocated (that would double-list the session).
step_resume() {
  resolve_transcript || true
  local sdir resume_id=""
  if [[ -n "$TRANSCRIPT" ]]; then
    sdir="$(dirname "$TRANSCRIPT")"
    resume_id="$(jq -r '.session_id // empty' "$sdir/meta.json" 2>/dev/null || true)"
  fi
  [[ -z "$resume_id" && -n "$UUID" ]] && resume_id="${UUID##*_}"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  SESSION="mistral-vibedrv-$$-$(date +%s)-resume${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"
  SES_ALIVE[$ACTIVE]=1
  echo "[driver] resume[s$ACTIVE]: relaunch vibe --resume $resume_id (same dir=$(daemon_sid "$TRANSCRIPT"))" >&2
  boot_vibe_active "" "$resume_id"
}

# step_start_session — launch a NEW concurrent vibe session WITHOUT tearing the
# active one down (the multiple-sessions-same-cwd contract). The prior session's
# tmux + process survive, so the daemon observes BOTH as independent rows; vibe
# keys each session on its own lazily-minted session_<ts>_<hash> dir (id is
# timestamp+hash-keyed, NOT cwd-keyed), so two concurrent sessions in the SAME
# cwd never merge. Defaults to session 1's cwd (the same-cwd scenario); pass a
# directory to launch elsewhere. Mirror of step_restart, minus the teardown.
step_start_session() {
  local req_cwd="$1"
  # Bind + freeze the current slot's transcript BEFORE spawning a concurrent
  # one. resolve_transcript here claims the prior slot's session dir so it is in
  # PRE_LAUNCH_DIRS when boot_vibe_active re-snapshots — otherwise the new slot's
  # set-diff in resolve_transcript could bind to the OLD (still-streaming) dir.
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  # Bare launch (no positional prompt): the following send/slash steps type into
  # the now-live REPL. boot_vibe_active's bare path waits for banner readiness, so
  # the concurrent session's input box is live before the next keystroke lands.
  alloc_slot "mistral-vibedrv-$$-$(date +%s)-start${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd, prior slots stay alive)" >&2
  boot_vibe_active "" ""
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
launch_repl
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  # Optional inline session target: switch the active slot to N before running
  # the step (send/wait_turn on {"session":1} route back to slot 1 while slot 2
  # stays alive). start_session is exempt — it allocates its own slot. A target
  # slot must already exist.
  tgt="$(jq -r '.session // empty' <<<"$step")"
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (sid=$(daemon_sid "$TRANSCRIPT"))" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"; break
    fi
  fi
  case "$type" in
    send)            send_text "$(jq -r '.text' <<<"$step")" ;;
    slash)           slash_cmd "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       step_interrupt ;;
    keys)            # Raw tmux key sequence (NOT literal text) for driving picker
                     # UIs — arrow keys / Enter for the /rewind RewindApp and the
                     # /model ModelPickerApp, C-c to clear a pre-filled input, etc.
                     # e.g. {"type":"keys","keys":"Down Enter"}.
                     ks="$(jq -r '.keys' <<<"$step")"
                     # shellcheck disable=SC2086 — intentional word-split of the key list
                     tmux send-keys -t "$SESSION" $ks
                     echo "[driver] keys[s$ACTIVE]: $ks" >&2
                     sleep 0.5 ;;
    reset_session)   not_implemented reset_session || break ;;   # TODO(mistral-vibe): in-REPL /clear|/new → new id, SAME slot; re-resolve SES_TRANSCRIPT[$ACTIVE] (SEAM 3)
    restart)         step_restart ;;
    resume)          step_resume ;;
    sigkill)         step_sigkill ;;
    exit_clean)      step_exit_clean ;;
    start_session)   step_start_session "$(jq -r '.cwd // empty' <<<"$step")" ;;
    session)         : ;;   # pure focus switch — already handled by the inline target block above
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract (shared) -------------------------------------
# A recipe with no wait_turn step (e.g. autonomous-loop, driven entirely by
# sleeps around /loop) never calls resolve_transcript during dispatch, so the
# active slot's transcript is still unbound. Bind it now — the session dir exists
# by end-of-run — so the contract carries a real session id instead of "missing".
resolve_transcript || true
# Persist the final active slot's view back into its slot so the multi-session
# session.uuids / transcript.paths lists carry EVERY slot (start_session's
# concurrent sessions), not just slot 1.
save_active
#
# emit_session_contract writes session.uuid + transcript.path (slot 1) plus the
# multi-session session.uuids / transcript.paths lists from SES_TRANSCRIPT, and
# combines the per-slot stdout (_lib/drive/contracts.sh). It needs each slot's
# SES_TRANSCRIPT[$i] populated by SEAM 3; until that's ported the paths are empty
# — the contract SHAPE is already correct, the scaffold just records nothing
# useful yet. The primary id is the daemon's session_id — daemon_sid of the
# transcript path; switch to the first-line UUID if mistral-vibe keys on that (see
# drive-pi-interactive.sh). drive_exit maps EXIT_REASON → the process exit code.
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"
drive_exit
