#!/usr/bin/env bash
# drive-antigravity-interactive.sh — drive antigravity's REPL via tmux, executing a
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
#   sed -i '' 's/antigravity/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(antigravity) below by
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
#   If antigravity has a true headless-per-turn mode (e.g. `antigravity run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if antigravity is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-antigravity-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-antigravity-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
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
DRIVE_MARKER_PREFIX="$STAGING/.antigravity-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"

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
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if antigravity is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
DRIVE_ELICITS="send slash wait_turn keys sleep exit_clean restart resume sigkill start_session session"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"

# View vars owned by the driver (the slot lib reads/writes these). SESSION is the
# active tmux session; TRANSCRIPT/UUID are the resolved transcript path + conv-id;
# EXPECTED_TURNS is the turn-done count wait_turn waits past; MARKER gates which
# brain conv dir resolve_transcript may claim for this slot.
SESSION=""
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
MARKER=""

# Antigravity's CLI brain store. A turn writes a NEW conversation dir
# <conv-id>/.system_generated/logs/transcript.jsonl; the conv-id directory name
# IS the daemon's session_id (SessionIDFromPath in the adapter).
ANTIGRAVITY_BRAIN_ROOT="${HOME}/.gemini/antigravity-cli/brain"
ANTIGRAVITY_TRANSCRIPT_REL=".system_generated/logs/transcript.jsonl"

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for antigravity — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
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
  # Optional extra args (e.g. --continue for resume) are passed through to agy.
  # The caller must have alloc_slot'd the active slot before calling — the boot
  # itself (trust prompt + input-ready wait) is identical across launch/restart/
  # resume, so they all share this body.
  local extra_args=("$@")
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  # ${arr[@]+"${arr[@]}"} expands to nothing when the array is empty WITHOUT
  # tripping `set -u` on bash 3.2 (macOS) — a bare "${arr[@]}" is "unbound".
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "agy" \
    ${extra_args[@]+"${extra_args[@]}"} \
    >>"$DRIVER_LOG.stdout.$ACTIVE" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch agy under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # agy mints the conversation dir + transcript only AFTER the first prompt lands,
  # so there's nothing to resolve at boot — resolve_transcript runs on the first
  # wait_turn (SEAM 3, marker-gated by alloc_slot's MARKER).
  #
  # A FRESH cwd (every recording uses a brand-new staging cwd/) triggers agy's
  # "Do you trust the contents of this project?" gate, which BLOCKS the REPL until
  # answered — "Yes, I trust this folder" is the default-highlighted choice, so a
  # bare Enter dismisses it. Without this the input box never appears, the first
  # send is typed into the trust dialog, and no turn ever happens. Accept it, THEN
  # wait for the "? for shortcuts" footer (the input-ready marker) so the first
  # send isn't swallowed, then settle briefly so Ink finishes mounting the prompt.
  local waited=0
  while (( waited < 40 )); do
    if tmux capture-pane -t "$SESSION" -p -S -50 2>/dev/null | grep -qiE 'trust the contents|trust this folder'; then
      tmux send-keys -t "$SESSION" Enter
      echo "[driver] accepted agy trust-folder prompt" >&2
      break
    fi
    # The footer can also appear directly (already-trusted cwd) — stop waiting then.
    tmux capture-pane -t "$SESSION" -p -S -50 2>/dev/null | grep -q '? for shortcuts' && break
    sleep 0.5; waited=$((waited + 1))
  done

  waited=0; local ready=0
  while (( waited < 60 )); do
    if tmux capture-pane -t "$SESSION" -p -S -50 2>/dev/null | grep -q '? for shortcuts'; then
      ready=1; break
    fi
    sleep 0.5; waited=$((waited + 1))
  done
  if (( ready )); then
    echo "[driver] agy input ready (slot=$ACTIVE, cwd=${SES_CWD[$ACTIVE]})" >&2
    sleep 1
  else
    echo "[driver] WARNING: agy input-ready marker not seen after 30s; proceeding" >&2
  fi
}

# transcript_claimed reports whether another slot already owns transcript path $1
# (so a concurrent slot / restart claims the NEXT-newest conv dir, not a sibling's).
transcript_claimed() { # <abs-path>
  local p="$1" i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ "${SES_TRANSCRIPT[$i]:-}" == "$p" ]] && return 0
  done
  return 1
}

# --- AGENT-SPECIFIC SEAM 3: resolve the transcript agy just created ----------
# agy writes a NEW conversation dir under $ANTIGRAVITY_BRAIN_ROOT only after the
# first user message lands. Find the newest <conv-id>/.system_generated/logs/
# transcript.jsonl created AFTER this slot's MARKER (so a restart, which bumps the
# marker, excludes the prior conv) that isn't bound to another slot. The conv-id
# DIRECTORY name is the daemon's session_id (SessionIDFromPath in the adapter),
# so UUID is harvested from the path, not the transcript body. Caches once resolved.
resolve_transcript() {
  [[ -n "$TRANSCRIPT" ]] && return 0
  # agy writes transcript.jsonl only once the first turn produces output, which on
  # the free tier can lag tens of seconds behind the send — poll generously (the
  # recipe timeout is the real backstop).
  local _ candidate="" conv_dir
  for _ in $(seq 1 120); do
    candidate=""
    # newest-first by mtime of the transcript; -newer "$MARKER" excludes prior convs.
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      transcript_claimed "$f" && continue
      candidate="$f"; break
    done < <(find "$ANTIGRAVITY_BRAIN_ROOT" -maxdepth 4 -type f \
                  -path "*/$ANTIGRAVITY_TRANSCRIPT_REL" -newer "$MARKER" \
                  -exec ls -t {} + 2>/dev/null)
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      # <brain>/<conv-id>/.system_generated/logs/transcript.jsonl → <conv-id>.
      conv_dir="${candidate%/.system_generated/logs/transcript.jsonl}"
      UUID="$(basename "$conv_dir")"
      TRANSCRIPT="$candidate"
      # Persist onto the slot so the staging contract (SES_TRANSCRIPT) sees it.
      SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
      SES_UUID[$ACTIVE]="$UUID"
      echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (conv-id=$UUID, sid=$UUID)" >&2
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# --- AGENT-SPECIFIC SEAM 2: count completed turns ----------------------------
# Mirror core/adapters/inbound/agents/antigravity/parser.go: the turn's terminal
# line is a MODEL PLANNER_RESPONSE with NO tool_calls (often empty content). A
# text-only answer and a tool-using turn both end this way; tool-calling and
# RESULT lines keep the session working and are excluded. SYSTEM steps
# (CONVERSATION_HISTORY, CHECKPOINT) can TRAIL the terminal line, so count the
# markers rather than inspect only the last line.
turn_count() {
  [[ -f "$TRANSCRIPT" ]] || { echo 0; return; }
  jq -r 'select(.source=="MODEL" and .type=="PLANNER_RESPONSE"
                and ((.tool_calls // []) | length) == 0) | "x"' \
    "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Block until a NEW terminal PLANNER_RESPONSE appears (turn_count >= the bumped
# EXPECTED_TURNS), or time out via remaining_seconds().
#
# agy 1.0.10 gates every run_command (and other write/exec tools) behind an
# in-turn "Requesting permission for: <cmd> / Do you want to proceed?" dialog —
# tool execution does NOT auto-run even for a plain `agy` launch (live-verified
# 2026-06-20: the turn stalls at the planner intent with no RUN_COMMAND result
# until answered). The dialog is UI-only; it is NOT written to transcript.jsonl,
# so the daemon never sees it (the session stays `working`, never `waiting`).
# Option 1 "Yes" is pre-highlighted, so a bare Enter grants it. A multi-tool turn
# fires the dialog once PER tool call, so accept it every time it is on screen
# (Enter on an empty input box mid-generation is a harmless no-op). This mirrors
# the claudecode driver's in-turn "Run a dynamic workflow?" auto-accept.
wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: agy never created a transcript under $ANTIGRAVITY_BRAIN_ROOT" >&2
    EXIT_REASON="transcript_missing"; return 1
  }
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  while (( $(remaining_seconds) > 0 )); do
    local now; now="$(turn_count)"
    if (( now >= EXPECTED_TURNS )); then
      echo "[driver] wait_turn[s$ACTIVE]: count=$now (expected ≥ $EXPECTED_TURNS)" >&2
      return 0
    fi
    if tmux capture-pane -t "$SESSION" -p -S -50 2>/dev/null \
         | grep -qiE 'Requesting permission for|Do you want to proceed'; then
      tmux send-keys -t "$SESSION" Enter
      echo "[driver] wait_turn[s$ACTIVE]: granted agy run_command permission dialog" >&2
    fi
    sleep 1
  done
  echo "[driver] wait_turn[s$ACTIVE]: timeout (count=$(turn_count), expected ≥ $EXPECTED_TURNS)" >&2
  EXIT_REASON="timeout"; return 1
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# Type the line, then CONFIRM it echoed into agy's input box before pressing
# Enter. agy's Ink input occasionally swallows the very first keystrokes after
# boot, leaving an empty prompt — Enter on an empty box does nothing, no turn
# happens, and wait_turn then times out with "never created a transcript". A
# distinctive substring of the text is the cheap, reliable echo probe; re-type
# once if it's missing.
send_text() { # <text>
  local probe; probe="${1:0:20}"
  tmux send-keys -t "$SESSION" -l -- "$1"
  sleep 0.5   # let agy's input render
  if ! tmux capture-pane -t "$SESSION" -p -S -10 2>/dev/null | grep -qF -- "$probe"; then
    echo "[driver] send_text: input not echoed, re-typing once" >&2
    tmux send-keys -t "$SESSION" -l -- "$1"
    sleep 0.5
  fi
  tmux send-keys -t "$SESSION" Enter
}

# --- AGENT-SPECIFIC SEAM: keys — raw tmux key sequence -----------------------
# Send a raw, word-split key list (e.g. "! e c h o Space h e l l o Enter") to the
# active REPL — for the `!cmd` shell-escape and mid-turn buffered typing, where a
# literal `send` of the line is wrong (the `!` prefix and the in-turn buffering
# both need raw keystrokes, not a probe-confirmed `send_text`). Mirrors the
# claudecode/gemini-cli step_keys: intentional word-splitting of the key list.
step_keys() { # <keys>
  local keys="$1"
  # shellcheck disable=SC2086 — intentional word-splitting of the key list
  tmux send-keys -t "$SESSION" $keys
  echo "[driver] keys[s$ACTIVE]: $keys" >&2
  sleep 0.3
}

# --- AGENT-SPECIFIC SEAM: find the agy PID the daemon binds ------------------
# Mirror core/adapters/inbound/agents/antigravity/pid.go: a plain process-name
# (agy) + cwd match, lowest PID — agy is a single native process per
# conversation, so no argv filtering. Fallback to the tmux pane's agy child so a
# SIGKILL can't merely orphan agy (an orphan keeps the cwd alive and the daemon
# would never observe process_exited).
agy_pid() {
  local cwd="${SES_CWD[$ACTIVE]}" best="" p pcwd
  for p in $(pgrep -x 'agy' 2>/dev/null); do
    pcwd="$(lsof -a -p "$p" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
    [[ "$pcwd" == "$cwd" ]] || continue
    if [[ -z "$best" || "$p" -lt "$best" ]]; then best="$p"; fi
  done
  if [[ -z "$best" ]]; then
    local pane_pid
    pane_pid=$(tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
    if [[ -n "$pane_pid" ]]; then
      best=$(pgrep -x 'agy' -P "$pane_pid" 2>/dev/null | head -1)
      [[ -z "$best" ]] && best="$pane_pid"
    fi
  fi
  echo "$best"
}

# --- AGENT-SPECIFIC SEAM: exit_clean — Ctrl-D graceful shutdown --------------
# agy's Ink REPL exits on Ctrl-D; the OS terminates the agy process and the
# daemon's process scanner emits process_exited. Sleep gives agy time to flush.
step_exit_clean() {
  resolve_transcript || true
  tmux send-keys -t "$SESSION" C-d
  sleep 2
  SES_ALIVE[$ACTIVE]=0
  echo "[driver] exit_clean[s$ACTIVE]: sent Ctrl-D to $SESSION" >&2
}

# --- AGENT-SPECIFIC SEAM: sigkill — kill -9 the active session's agy PID ------
step_sigkill() {
  resolve_transcript || true
  local pid; pid="$(agy_pid)"
  if [[ -n "$pid" ]]; then
    kill -9 "$pid" 2>/dev/null || true
    echo "[driver] sigkill[s$ACTIVE]: killed PID $pid (conv-id=$UUID)" >&2
  else
    echo "[driver] sigkill[s$ACTIVE]: no agy PID found (cwd=${SES_CWD[$ACTIVE]}, session=$SESSION)" >&2
  fi
  SES_ALIVE[$ACTIVE]=0
  # Leave the dead tmux pane for teardown — the kill alone produces process_exited.
  sleep 1
}

# --- AGENT-SPECIFIC SEAM: restart — end this session, start a FRESH one -------
# A new session id (new conv-id), new chats dir, fresh cwd. Used between
# session-end variants so each lands as its own session row separated by a grey
# gap. By the time restart runs the active process is usually already gone (an
# exit_clean or sigkill preceded it); retire the slot regardless but keep it in
# the list so the epilogue flushes its conv-id. A fresh cwd keeps each variant's
# brain dir cleanly separated and gives it its own trust state.
step_restart() {
  resolve_transcript || true
  save_active
  SES_ALIVE[$ACTIVE]=0
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  local idx=$(( N_SLOTS + 1 ))
  alloc_slot "antigravitydrv-$$-$(date +%s)-${idx}" "${RUN_CWD}-${idx}"
  mkdir -p "${SES_CWD[$ACTIVE]}"
  SES_CWD[$ACTIVE]="$(cd "${SES_CWD[$ACTIVE]}" && pwd -P)"
  echo "[driver] restart: new session slot #${ACTIVE} (cwd=${SES_CWD[$ACTIVE]})" >&2
  launch_repl
}

# --- AGENT-SPECIFIC SEAM: resume — relaunch agy --continue in the SAME cwd ----
# agy --continue resumes the MOST RECENT conversation in the cwd: it APPENDS to
# the SAME <conv-id>/transcript.jsonl across both PID lifetimes (live-verified),
# so the daemon sees the SAME session_id re-appear, not a new row. This is ONE
# session (one slot) with two process lifetimes: TRANSCRIPT/UUID/MARKER stay
# unchanged and we do NOT alloc a new slot (which would double-list the conv-id
# at curate time). Only the tmux session name rotates; the cwd is reused so
# DiscoverPID rebinds the new PID to the same session.
step_resume() {
  resolve_transcript || true
  # If exit_clean didn't precede this, end the running agy cleanly first.
  if [[ "${SES_ALIVE[$ACTIVE]}" == "1" ]]; then
    tmux send-keys -t "$SESSION" C-d
    sleep 2
  fi
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  sleep 1
  SESSION="antigravitydrv-$$-$(date +%s)-r${ACTIVE}"
  SES_SESSION[$ACTIVE]="$SESSION"
  SES_ALIVE[$ACTIVE]=1
  # Keep the SAME transcript cached across the relaunch: agy appends to it rather
  # than minting a new file, so resolve_transcript must NOT run again (clearing +
  # re-finding could race the new process / claim a sibling). TRANSCRIPT/UUID/
  # EXPECTED_TURNS stay as-is.
  echo "[driver] resume[s$ACTIVE]: relaunch agy --continue (same conv-id=$UUID, cwd=${SES_CWD[$ACTIVE]})" >&2
  launch_repl --continue
}

# --- AGENT-SPECIFIC SEAM: start_session — launch a CONCURRENT agy -------------
# Launch a NEW agy session WITHOUT tearing the active one down: the previous slot
# keeps running (its tmux survives), so the daemon observes both as independent
# session rows. Defaults to slot 1's cwd (the same-cwd scenario); pass a directory
# to launch elsewhere. Claim the active slot's transcript BEFORE spawning the
# concurrent one — if the prior slot is unresolved, a turn still streaming there
# keeps bumping its mtime past the new slot's just-touched marker, and the new
# slot's resolve_transcript could bind to the OLD conv dir. Resolving here marks
# it claimed so transcript_claimed excludes it from the new slot's discovery.
step_start_session() { # [cwd]
  local req_cwd="$1"
  resolve_transcript || true
  save_active
  local idx=$(( N_SLOTS + 1 ))
  local new_cwd="${req_cwd:-$RUN_CWD}"
  mkdir -p "$new_cwd"
  new_cwd="$(cd "$new_cwd" && pwd -P)"
  alloc_slot "antigravitydrv-$$-$(date +%s)-${idx}" "$new_cwd"
  echo "[driver] start_session: concurrent session slot #${ACTIVE} (cwd=$new_cwd)" >&2
  launch_repl
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
# Bring up the first session as slot 1. restart allocates further slots; resume
# relaunches in place (same slot, same conv-id).
alloc_slot "antigravitydrv-$$-$(date +%s)" "$RUN_CWD"
launch_repl
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  # Optional inline session target: switch the active slot to N before running
  # the step (mirrors the codex driver). start_session is exempt — it allocs its
  # own slot. The target slot must already exist.
  tgt="$(jq -r '.session // empty' <<<"$step")"
  if [[ -n "$tgt" && "$type" != "start_session" && "$tgt" != "$ACTIVE" ]]; then
    if [[ "$tgt" =~ ^[0-9]+$ && "$tgt" -ge 1 && "$tgt" -le "$N_SLOTS" ]]; then
      save_active
      load_slot "$tgt"
      echo "[driver] switch -> session slot $tgt (conv-id=$UUID)" >&2
    else
      echo "[driver] switch: invalid session slot '$tgt' (have $N_SLOTS)" >&2
      EXIT_REASON="nonzero(2)"; break
    fi
  fi
  case "$type" in
    send|slash)      send_text "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       not_implemented interrupt || break ;;       # TODO(antigravity): Escape/Ctrl-C the in-flight turn
    keys)            step_keys "$(jq -r '.keys' <<<"$step")" ;;
    reset_session)   not_implemented reset_session || break ;;   # TODO(antigravity): in-REPL /clear|/new → new id, SAME slot; re-resolve SES_TRANSCRIPT[$ACTIVE] (SEAM 3)
    restart)         step_restart ;;
    resume)          step_resume ;;
    sigkill)         step_sigkill ;;
    exit_clean)      step_exit_clean ;;
    start_session)   step_start_session "$(jq -r '.cwd // empty' <<<"$step")" ;;
    session)         : ;;   # pure focus switch — already handled by the inline target block
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract (shared) -------------------------------------
# Persist the live view back to the active slot so multi-session runs flush every
# conv-id (restart allocs new slots; the loop's last save was the active one).
save_active
# emit_session_contract writes session.uuid + transcript.path (slot 1) plus the
# multi-session session.uuids / transcript.paths lists, and combines the per-slot
# stdout (_lib/drive/contracts.sh). For antigravity the daemon's session_id is the
# <conv-id> DIRECTORY (sessionIDFromPath in the adapter), NOT daemon_sid of the
# constant transcript.jsonl filename ("transcript") — so pass the resolved conv-id
# from slot 1 (SES_UUID[1]) as the primary id, then OVERRIDE the session.uuids list
# (which emit_ wrote as daemon_sid = "transcript" for every slot) with the conv-ids.
emit_session_contract "${SES_UUID[1]}"
: > "$STAGING/session.uuids"
for (( i = 1; i <= N_SLOTS; i++ )); do
  echo "${SES_UUID[$i]}" >> "$STAGING/session.uuids"
done
drive_exit
