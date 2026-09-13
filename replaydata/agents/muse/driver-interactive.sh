#!/usr/bin/env bash
# drive-muse-interactive.sh — drive muse's REPL via tmux, executing a
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
#   sed -i '' 's/muse/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(muse) below by
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
#   If muse has a true headless-per-turn mode (e.g. `muse run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if muse is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-muse-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-muse-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# shellcheck disable=SC2034  # positional arg $2 of the driver protocol (tools/onboarding-factory/scripts/run-cell.sh:379); read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot)
UUID="$2"            # preferred session id; some agents mint their own (ignore then)
TIMEOUT_S="$3"
# shellcheck disable=SC2034  # positional arg $4 of the driver protocol (tools/onboarding-factory/scripts/run-cell.sh:379); named so the slot is not silently reused, though this adapter's launch does not read it
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
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh:56 (alloc_slot)
DRIVE_MARKER_PREFIX="$STAGING/.muse-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"

# Slot state the lib reads/writes (the driver owns these globals). A run starts
# with zero slots; launch_repl allocs slot 1, and restart/start_session alloc
# more. ACTIVE indexes the live slot; SESSION/TRANSCRIPT/UUID mirror it.
N_SLOTS=0; ACTIVE=0
# Active-session view (pi pattern): step functions read/write these, kept in
# sync via save_active/load_slot. TRANSCRIPT is the absolute session.jsonl
# path; UUID is muse's own session id (the session dir basename == stream.id,
# NOT the preferred $2 UUID — muse mints its own, like pi).
TRANSCRIPT=""
UUID=""
EXPECTED_TURNS=0
SES_SESSION=()
SES_TRANSCRIPT=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_UUID=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_EXPECTED=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_MARKER=()
SES_CWD=()
# shellcheck disable=SC2034  # driver-owned slot array written by the sourced replaydata/_lib/drive/slots.sh:66 (alloc_slot); kept current here for the shared slot model. Named for OWNERSHIP, not liveness (#1828): nothing re-derives it from `tmux has-session`, so teardown gates on session-name presence and never on this.
SES_OWNED=()

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Start with ONLY the seams
# that actually work in this scaffold (send/slash/sleep) and add each primitive
# as you port its seam — a stubbed `not_implemented` arm must NOT be listed, so
# recipe-lint flags a recipe needing it as a semantic_gap before recording. Set
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if muse is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:97 (sed), never expanded in shell
DRIVE_ELICITS="send slash wait_turn sleep"
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:113 (sed), never expanded in shell
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonicalize (resolve symlinks) for the daemon's cwd match
DEADLINE=$(( $(date +%s) + TIMEOUT_S ))
EXIT_REASON="ok"
# Raised to 1 by the epilogue, immediately before the final exit. cleanup() reads
# it to tell a run that FINISHED (EXIT_REASON is its verdict) apart from a `set
# -e` abort that never formed one (#1825) — see cleanup().
REACHED_EPILOGUE=0
SESSION=""

remaining_seconds() { local now; now=$(date +%s); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }

not_implemented() { # <step-type>
  echo "[driver] STUB: step type '$1' not yet ported for muse — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down if a session was
# started. Set EXIT_REASON before a failing `exit` so the reason is accurate.
#
# THE REACHED_EPILOGUE GUARD IS NOT OPTIONAL, and it is the half that the
# "set EXIT_REASON before a failing `exit`" sentence above does NOT cover
# (#1825). That discipline is per-`exit`: it works for the exits this file
# writes, and says nothing about a `set -e` abort, which is every other command
# in the driver. A driver with no trap at all wrote NO driver.exit-reason on an
# abort and run-cell.sh:418 read it as "unknown" — accurate, if unhelpful. A
# trap that writes "$EXIT_REASON" unconditionally turns that "unknown" into
# `ok`: the INITIAL value, reported as a verdict by a run that never formed one.
# That is the same "reports success while it actually failed" shape #1825 was
# opened for, reintroduced by the fix for it. So an abort that never reached the
# epilogue, with EXIT_REASON still at its initial `ok`, is recorded as a driver
# fault instead. A verdict already formed (timeout, readiness_timeout, …) is
# left exactly as the step that formed it set it.
#
# The other half of the contract is the epilogue raising REACHED_EPILOGUE=1
# immediately before its final exit. Port BOTH halves or neither: the flag
# without the guard does nothing, and the guard without the flag reports every
# successful run as a driver fault.
# BEGIN cleanup
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  # An abort that never reached the epilogue never formed a verdict, so
  # EXIT_REASON is still its initial "ok". Writing that would report SUCCESS for
  # a failed run — the exact shape #1825 exists to stop — so record the
  # driver-fault reason instead. A verdict already formed (timeout, …) stands.
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="nonzero(2)"
    echo "[driver] aborted before the epilogue — recording exit reason $EXIT_REASON, not ok" >&2
  fi
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
# END cleanup
trap cleanup EXIT

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# `muse` with no subcommand starts the interactive TUI (`muse --help`: "If no
# subcommand is given, options run the interactive TUI"). Launched detached in
# $RUN_CWD, stdout/stderr to "$DRIVER_LOG.stdout|.stderr". muse mints its own
# session UUID, so the preferred $2 UUID is ignored and transcript resolution
# is deferred to step_wait_turn (SEAM 3, pi pattern) — the session dir may only
# materialize after the first prompt lands. NEVER pass --no-session-log: it
# disables the session.jsonl persistence the daemon tails.
# (`muse exec` is a true headless-per-turn mode; a future hybrid shape like
# drive-opencode-interactive.sh could use it, but the scaffold stays REPL-only.)
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # alloc_slot mints a fresh slot, points SESSION at its tmux name and ACTIVE at
  # it, and clears the slot's TRANSCRIPT/UUID. restart/start_session call it again
  # to open another session; per-slot stdout (.stdout.$ACTIVE) feeds the contract.
  alloc_slot "musedrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" "muse" \
    >>"$DRIVER_LOG.stdout.$ACTIVE" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch muse under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
}

# muse persists sessions under $MUSE_DATA_ROOT/sessions/<YYYY>/<MM>/<DD>/<uuid>/
# (default $HOME/.local/share/muse — observed on this machine; no data-root
# override flag exists in `muse --help`, only --no-session-log, which the
# driver must never pass). MUSE_DATA_ROOT is therefore a DRIVER-level override
# for transcript resolution only, and recordings land in the maintainer's live
# store until muse gains a relocatable root (cf. CODEX_HOME / HERMES_HOME).
MUSE_DATA_ROOT="${MUSE_DATA_ROOT:-$HOME/.local/share/muse}"

# --- AGENT-SPECIFIC SEAM 3: resolve the transcript muse just created ---------
# The session dir appears at/after launch; the session.jsonl inside grows with
# every event. Find the newest session.jsonl NEWER than this slot's $MARKER
# (pi pattern — a resume/restart bumps the marker and excludes the prior file).
# The session id is the parent dir basename (== stream.id on every observed
# session.jsonl); the file basename is always "session.jsonl", so daemon_sid
# of the transcript path is useless — the epilogue passes SES_UUID instead.
resolve_transcript() {
  if [[ -n "$TRANSCRIPT" ]]; then return 0; fi
  for _ in $(seq 1 60); do
    local candidate=""
    candidate="$(find "$MUSE_DATA_ROOT/sessions" -type f -name 'session.jsonl' \
                  -newer "$MARKER" 2>/dev/null | sort | tail -n1)"
    if [[ -n "$candidate" && -s "$candidate" ]]; then
      # Sanity: first line must be a JSON record with this session's stream id.
      local sid=""
      sid="$(basename "$(dirname "$candidate")")"
      if head -n1 "$candidate" | jq -e . >/dev/null 2>&1; then
        TRANSCRIPT="$candidate"
        UUID="$sid"
        SES_TRANSCRIPT[$ACTIVE]="$TRANSCRIPT"
        SES_UUID[$ACTIVE]="$UUID"
        echo "[driver] resolve_transcript[s$ACTIVE]: $TRANSCRIPT (uuid=$UUID)" >&2
        return 0
      fi
      sleep 0.5; continue
    fi
    sleep 0.5
  done
  return 1
}

# --- AGENT-SPECIFIC SEAM 2: count completed turns -----------------------------
# Canonical muse turn-done shape, read off live session.jsonl files on this
# machine (4 sessions, 678 records, muse 1.2.1): one user prompt opens exactly
# one run (payload_type=runtime.session, kind=run, event=started, with its own
# run_id) and the run's last word is event=terminal (carries turn_duration_ms;
# terminal=cancelled when the turn was interrupted). model_completed fires per
# model call and task/completed per sub-task — both too granular to gate on.
# The Go adapter's parser is canonical once it lands; until then this jq count
# mirrors it. Kept inline (not a sibling turn-count.sh) until record proves it
# live — extraction is record's call, per the sparsest-grammar rule.
turn_count() {
  if [[ -f "$TRANSCRIPT" ]]; then
    jq -r 'select(.payload_type=="runtime.session" and ((.payload // {}).kind=="run") and ((((.payload // {}).event) // {}).kind=="terminal")) | "x"' \
      "$TRANSCRIPT" 2>/dev/null | wc -l | tr -d ' '
  else
    echo 0
  fi
}

# Track expected vs. actual completed turns (pi pattern): each `send` bumps
# EXPECTED_TURNS by 1; wait_turn waits for actual >= expected. Without this a
# fast turn can complete before resolve_transcript returns and a naive
# before/after snapshot waits forever. Slash commands bill a turn the same as
# sends until record proves muse handles a no-LLM slash locally (cf. 2.5).
step_send() { # <text>
  local text="$1"
  tmux send-keys -t "$SESSION" -l -- "$text"
  tmux send-keys -t "$SESSION" Enter
  EXPECTED_TURNS=$((EXPECTED_TURNS + 1))
  echo "[driver] send[s$ACTIVE]: ${text:0:60} (expecting turn $EXPECTED_TURNS)" >&2
}

step_wait_turn() {
  resolve_transcript || {
    echo "[driver] wait_turn[s$ACTIVE]: muse never created a transcript under $MUSE_DATA_ROOT/sessions" >&2
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

wait_turn() {
  step_wait_turn
}

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
send_text() { # <text>
  tmux send-keys -t "$SESSION" -l "$1"
  tmux send-keys -t "$SESSION" Enter
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
launch_repl
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot)
EXPECTED_TURNS=0
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    send|slash)      step_send "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       step_wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       not_implemented interrupt || break ;;       # TODO(muse): Escape/Ctrl-C the in-flight turn
    keys)            not_implemented keys || break ;;            # TODO(muse): tmux send-keys raw sequence
    reset_session)   not_implemented reset_session || break ;;   # TODO(muse): in-REPL /clear|/new → new id, SAME slot; re-resolve SES_TRANSCRIPT[$ACTIVE] (SEAM 3)
    restart)         not_implemented restart || break ;;         # TODO(muse): save_active; alloc_slot <name> <new-cwd>; launch — new slot carries the new id
    resume)          not_implemented resume || break ;;          # TODO(muse): relaunch same id+cwd (1 session, 2 PIDs) — reuse the active slot
    sigkill)         not_implemented sigkill || break ;;         # TODO(muse): kill -9 the active slot's PID
    exit_clean)      not_implemented exit_clean || break ;;      # TODO(muse): Ctrl-D graceful shutdown
    start_session)   not_implemented start_session || break ;;   # TODO(muse): save_active; alloc_slot; launch a CONCURRENT session, keep the first alive
    session)         not_implemented session || break ;;         # TODO(muse): save_active; load_slot N — switch the active slot
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Write the staging contract (shared) -------------------------------------
# Persist the final active state, then best-effort resolve any slot that never
# got a transcript (e.g. a script with no wait_turn for that session) so
# session.uuids + transcript.paths are populated (pi pattern).
save_active
for (( i = 1; i <= N_SLOTS; i++ )); do
  if [[ -z "${SES_TRANSCRIPT[$i]}" ]]; then
    load_slot "$i"
    resolve_transcript || true
    save_active
  fi
done
# Staging contract: muse's primary session.uuid is the agent-side UUID (the
# session dir basename) for fixture-naming parity — run-cell.sh re-maps it to
# the daemon's session_id via its transcript_path lookup. daemon_sid of a muse
# transcript path is always the constant "session" (basename session.jsonl),
# so unlike gemini the shared uuids list CANNOT carry daemon-side ids until
# the Go adapter defines muse's session_id — record revisits this with the
# parser. drive_exit maps EXIT_REASON → the process exit code.
emit_session_contract "${SES_UUID[1]}"
echo "drive-muse-interactive: $EXIT_REASON (slots=${N_SLOTS}, primary=${SES_UUID[1]}, transcript=${SES_TRANSCRIPT[1]})"
# The epilogue completed: EXIT_REASON is this run's real verdict, so cleanup()
# must record it as-is rather than rewrite it as an abort.
REACHED_EPILOGUE=1
drive_exit
