#!/usr/bin/env bash
# drive-copilot-interactive.sh — drive copilot's REPL via tmux, executing a
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
#   sed -i '' 's/copilot/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(copilot) below by
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
#   If copilot has a true headless-per-turn mode (e.g. `copilot run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if copilot is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-copilot-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-copilot-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
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
DRIVE_MARKER_PREFIX="$STAGING/.copilot-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/dialogs.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/teardown.sh"

# Copilot writes a CONSTANT transcript basename (events.jsonl), so the daemon
# keys the session on the PARENT DIRECTORY — see the adapter's
# sessionIDFromPath. slots.sh's default (filename stem) would hand every
# session the id "events", collapsing the whole run onto one fixture. Override
# after sourcing, the same way mistral-vibe does.
daemon_sid() {
  local p="$1"
  [[ -z "$p" ]] && { echo ""; return; }
  basename "$(dirname "$p")"
}

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
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if copilot is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
DRIVE_ELICITS="send slash sleep wait_turn exit_clean sigkill interrupt keys restart start_session session"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
SESSION=""

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for copilot — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
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
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # alloc_slot mints a fresh slot, points SESSION at its tmux name and ACTIVE at
  # it, and clears the slot's TRANSCRIPT/UUID. restart/start_session call it again
  # to open another session; per-slot stdout (.stdout.$ACTIVE) feeds the contract.
  alloc_slot "copilotdrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  seed_copilot_home
  # Snapshot the store BEFORE launching so resolve_transcript can take a set
  # difference rather than trusting newest-mtime — a concurrent session (or a
  # leftover from an earlier slot) makes "newest" the wrong answer.
  PRE_LAUNCH_DIRS="$(ls "$COPILOT_HOME/session-state" 2>/dev/null | sort || true)"
  local args=(--allow-all --no-color)
  [[ -n "${COPILOT_AVAILABLE_TOOLS:-}" ]] && args+=("--available-tools=$COPILOT_AVAILABLE_TOOLS")
  [[ -n "${RESUME_ID:-}" ]] && args+=("--resume=$RESUME_ID")
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" copilot "${args[@]}" \
    >>"$DRIVER_LOG.stdout.$ACTIVE" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch copilot under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux pipe-pane -t "$SESSION" -o "cat >> '$DRIVER_LOG.stdout.$ACTIVE'" 2>/dev/null || true
  wait_ready
  resolve_transcript || true
}

# wait_ready blocks until the TUI is actually accepting input. Typing before
# the input box exists is silently dropped — the keystrokes land in a redrawing
# screen and the turn never starts, which presents as a session directory with
# a workspace.yaml but no events.jsonl at all. The footer hint ("/ commands")
# is drawn once the prompt is live.
wait_ready() {
  local i pane
  for (( i = 0; i < 80; i++ )); do
    pane="$(tmux capture-pane -t "$SESSION" -p 2>/dev/null || true)"
    # The folder-trust modal blocks the input box entirely. Seeding
    # trustedFolders into config.json is NOT sufficient — verified on 1.0.77,
    # where the modal still appeared for a directory already listed there — so
    # answer it. "Yes" is the preselected option, hence a bare Enter.
    if grep -q 'trust the files in this folder' <<<"$pane"; then
      tmux send-keys -t "$SESSION" Enter
      sleep 1
      continue
    fi
    # The footer hint is only drawn once the prompt is accepting input.
    if grep -q '/ commands' <<<"$pane"; then
      sleep 0.5
      return 0
    fi
    sleep 0.5
  done
  return 0
}

# seed_copilot_home isolates all Copilot state under the staging dir and
# pre-answers the two first-run prompts that would otherwise block the REPL
# before it ever accepts a prompt:
#
#   trustedFolders     — "Do you trust the files in this folder?" (a modal that
#                        appears BEFORE the input box, so a driver that just
#                        types would hang until timeout)
#   askedSetupTerminals — the offer to rewrite the terminal's keybindings
#
# COPILOT_HOME is the supported isolation lever; --config-dir still works but
# warns that it is deprecated. Authentication is unaffected because credentials
# live in the OS keychain, not this directory — so a scratch home needs no
# re-login (verified on CLI 1.0.77).
seed_copilot_home() {
  export COPILOT_HOME="${COPILOT_HOME:-$STAGING/copilot-home}"
  mkdir -p "$COPILOT_HOME"
  # Written fresh each launch: RUN_CWD differs per slot for restart/start_session.
  cat > "$COPILOT_HOME/config.json" <<JSON
{
  "trustedFolders": ["$RUN_CWD", "${SES_CWD[$ACTIVE]}"],
  "askedSetupTerminals": true,
  "banner": "never"
}
JSON
}

# --- AGENT-SPECIFIC SEAM 3: resolve the session id + transcript --------------
# A Copilot session is a NEW directory under session-state/, created when the
# first prompt is sent (the directory holds workspace.yaml immediately but
# events.jsonl only once the turn starts). Resolve by set difference against the
# pre-launch snapshot, and require events.jsonl to exist so the contract never
# points at a transcript-less directory.
resolve_transcript() {
  local root="$COPILOT_HOME/session-state" now id i
  if [[ -n "${TRANSCRIPT:-}" ]]; then
    return 0
  fi
  for (( i = 0; i < 40; i++ )); do
    now="$(ls "$root" 2>/dev/null | sort || true)"
    id="$(comm -13 <(printf '%s\n' "$PRE_LAUNCH_DIRS") <(printf '%s\n' "$now") 2>/dev/null | grep -v '^$' | head -n1)"
    # Resolve on the DIRECTORY, which Copilot creates at launch (holding
    # workspace.yaml), NOT on events.jsonl — that file does not exist until the
    # first prompt is sent, so waiting for it here would block the very step
    # that creates it. turn_count tolerates the transcript not existing yet.
    if [[ -n "$id" && -d "$root/$id" ]]; then
      TRANSCRIPT="$root/$id/events.jsonl"
      UUID="$id"
      SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
      SES_UUID[$ACTIVE]="$UUID"
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Port the agent's turn-done signal: claudecode polls the transcript for
# stop_reason=="end_turn"; codex polls the rollout for task_complete; opencode
# polls the SQLite store for a step-finish. Return 0 when a NEW turn completed
# (or times out via remaining_seconds()).
# turn_count counts COMPLETED turns. Copilot writes an explicit
# assistant.turn_end per turn (with a turnId), so this is an exact count rather
# than the heuristic tail-quietness other drivers settle for.
turn_count() {
  local t="${TRANSCRIPT:-}"
  [[ -n "$t" && -f "$t" ]] || { echo 0; return; }
  grep -c '"type":"assistant\.turn_end"' "$t" 2>/dev/null || echo 0
}

wait_turn() {
  resolve_transcript || true
  local deadline now
  deadline=$(( $(date +%s) + $(remaining_seconds) ))
  while (( $(date +%s) < deadline )); do
    resolve_transcript >/dev/null 2>&1 || true
    now="$(turn_count)"
    if (( now >= EXPECTED_TURNS )); then
      return 0
    fi
    # --allow-all should pre-approve everything, but a subagent's shell can
    # still raise a prompt; dismissing keeps a recording from dying on it.
    dismiss_dialog_if_visible "$SESSION" 'Allow|Do you want to|trust the files' || true
    sleep 1
  done
  EXIT_REASON="timeout"
  return 1
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# type_enter types literally then submits. The brief pause matters: Copilot's
# TUI redraws on input and swallows an Enter delivered in the same tick.
type_enter() { # <text>
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
}

# send_text submits a prompt and records that one more turn is expected, which
# is what wait_turn polls against. Slash commands are handled separately by
# slash_cmd because most of them elicit no assistant turn at all.
send_text() { # <text>
  type_enter "$1"
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  SES_EXPECTED[$ACTIVE]=$EXPECTED_TURNS
}

# slash_cmd runs an in-REPL slash command. It deliberately does NOT bump
# EXPECTED_TURNS: /usage, /model and friends render locally and write no
# assistant.turn_end, so counting one would hang the next wait_turn until
# timeout.
slash_cmd() { # <text>
  type_enter "$1"
}

# active_pid returns the PID of the tmux pane running this slot's agent.
active_pid() {
  tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -n1
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
launch_repl
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    send)            send_text "$(jq -r '.text' <<<"$step")" ;;
    slash)           slash_cmd "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    # Esc cancels the in-flight turn. The turn it would have completed never
    # lands, so drop the expectation or every later wait_turn waits forever.
    interrupt)       tmux send-keys -t "$SESSION" Escape
                     (( EXPECTED_TURNS > 0 )) && EXPECTED_TURNS=$((EXPECTED_TURNS - 1))
                     SES_EXPECTED[$ACTIVE]=$EXPECTED_TURNS ;;
    keys)            tmux send-keys -t "$SESSION" "$(jq -r '.text' <<<"$step")" ;;
    reset_session)   not_implemented reset_session || break ;;   # /clear vs /new rotation is UNVERIFIED for copilot — left stubbed and out of DRIVE_ELICITS so recipe-lint refuses rather than guessing
    resume)          not_implemented resume || break ;;          # --resume=<id> is wired in launch_repl via RESUME_ID but the 1-session/2-PID contract is unverified
    restart)         save_active
                     tmux kill-session -t "$SESSION" 2>/dev/null || true
                     wait_tmux_session_gone "$SESSION" 15 || true
                     alloc_slot "copilotdrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
                     launch_repl ;;
    sigkill)         sigkill_and_wait "$(active_pid)" 15 || true
                     SES_ALIVE[$ACTIVE]=0 ;;
    # Copilot exits on /exit; Ctrl-D is not a documented quit path here.
    exit_clean)      slash_cmd "/exit"
                     wait_tmux_session_gone "$SESSION" 15 || true
                     SES_ALIVE[$ACTIVE]=0 ;;
    start_session)   save_active
                     alloc_slot "copilotdrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
                     launch_repl ;;
    session)         save_active; load_slot "$(jq -r '.session // 1' <<<"$step")" ;;
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract (shared) -------------------------------------
# emit_session_contract writes session.uuid + transcript.path (slot 1) plus the
# multi-session session.uuids / transcript.paths lists from SES_TRANSCRIPT, and
# combines the per-slot stdout (_lib/drive/contracts.sh). It needs each slot's
# SES_TRANSCRIPT[$i] populated by SEAM 3; until that's ported the paths are empty
# — the contract SHAPE is already correct, the scaffold just records nothing
# useful yet. The primary id is the daemon's session_id — daemon_sid of the
# transcript path; switch to the first-line UUID if copilot keys on that (see
# drive-pi-interactive.sh). drive_exit maps EXIT_REASON → the process exit code.
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"
drive_exit
