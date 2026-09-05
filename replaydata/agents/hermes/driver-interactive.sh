#!/usr/bin/env bash
# drive-hermes-interactive.sh — drive hermes's REPL via tmux, executing a
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
#   sed -i '' 's/hermes/<agent>/g' scripts/drive-<agent>-interactive.sh
#   chmod +x scripts/drive-<agent>-interactive.sh
# Then fill the three AGENT-SPECIFIC SEAMS marked TODO(hermes) below by
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
#   If hermes has a true headless-per-turn mode (e.g. `hermes run -p …`
#   that blocks until the turn ends), a tmux-REPL driver may be overkill for
#   the happy path — model the headless path like drive-opencode-interactive.sh
#   instead, where `send` launches a subprocess and `wait_turn` is a no-op. BUT:
#   headless modes usually CANNOT deliver in-REPL slash commands or signals
#   (opencode stores `/new` as literal text), so reset_session/slash/interrupt
#   still need a live-TUI path. opencode's driver carries BOTH: a headless path
#   and a run_live() tmux path the dispatcher picks when a recipe needs a TUI
#   primitive. Copy that hybrid shape if hermes is headless-first.
# ----------------------------------------------------------------------------
#
# Staging contract (identical across all drivers — do NOT change these names):
#   driver.log[.stdout|.stderr]  — captured CLI output
#   driver.exit-reason           — ok | timeout | killed | nonzero(N)
#   session.uuid / session.uuids — the session id(s) the daemon will key on
#   transcript.path / transcript.paths — absolute path(s) to the transcript(s)
#
# Usage:
#   drive-hermes-interactive.sh <staging-dir> <session-uuid> \
#       <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: drive-hermes-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# shellcheck disable=SC2034  # positional arg $2 of the driver protocol (tools/onboarding-factory/scripts/run-cell.sh:379); read by the sourced replaydata/_lib/drive/slots.sh (save_active/load_slot)
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
# shellcheck disable=SC2034  # read by the sourced replaydata/_lib/drive/slots.sh:56 (alloc_slot)
DRIVE_MARKER_PREFIX="$STAGING/.hermes-marker"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/slots.sh"
# shellcheck source=/dev/null
source "$_DRIVE_LIB/contracts.sh"

# Slot state the lib reads/writes (the driver owns these globals). A run starts
# with zero slots; launch_repl allocs slot 1, and restart/start_session alloc
# more. ACTIVE indexes the live slot; SESSION/TRANSCRIPT/UUID mirror it.
N_SLOTS=0; ACTIVE=0
SES_SESSION=()
SES_TRANSCRIPT=()
SES_UUID=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_EXPECTED=()
# shellcheck disable=SC2034  # driver-owned slot array; the sourced replaydata/_lib/drive/slots.sh reads it (save_active/load_slot/alloc_slot)
SES_MARKER=()
SES_CWD=()
# shellcheck disable=SC2034  # driver-owned slot array written by the sourced replaydata/_lib/drive/slots.sh:64 (alloc_slot); kept current here for the shared slot model
SES_OWNED=()
SES_LAUNCH_TS=()

# recipe-lint contract (#508 #4): the step types this driver genuinely ELICITS,
# read directly by recipe-lint (no separate manifest). Start with ONLY the seams
# that actually work in this scaffold (send/slash/sleep) and add each primitive
# as you port its seam — a stubbed `not_implemented` arm must NOT be listed, so
# recipe-lint flags a recipe needing it as a semantic_gap before recording. Set
# DRIVE_SLASH_REQUIRES_STEP_TYPE=true if hermes is headless-first (a bare
# send "/cmd" stores literal text instead of reaching the REPL).
# shellcheck disable=SC2034  # scraped from this file's SOURCE by tools/onboarding-factory/scripts/lib/recipe-lint.sh:97 (sed), never expanded in shell
DRIVE_ELICITS="send slash sleep wait_turn"
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
  echo "[driver] STUB: step type '$1' not yet ported for hermes — see scripts/templates/drive-interactive.sh.tmpl and drive-claudecode-interactive.sh" >&2
  EXIT_REASON="nonzero(3)"
  return 3
}

# Always honor the staging contract: write driver.exit-reason on ANY exit
# (including a `set -e` abort mid-launch) and tear tmux down if a session was
# started. Set EXIT_REASON before a failing `exit` so the reason is accurate.
# BEGIN cleanup
cleanup() {
  local i
  for (( i = 1; i <= N_SLOTS; i++ )); do
    [[ -n "${SES_SESSION[$i]:-}" ]] && tmux kill-session -t "${SES_SESSION[$i]}" 2>/dev/null || true
  done
  restore_settings
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

# --- Scenario settings → <HERMES_HOME>/config.yaml ---------------------------
# run-cell.sh writes the recipe's `settings` blob to $STAGING/settings.json and
# hands it to every driver as $4. Hermes reads no per-run settings file — its
# only configuration surface is <HERMES_HOME>/config.yaml — so a scenario that
# needs a config key had no way to get one and the blob was silently dropped.
# That is not cosmetic: 2.8 autonomous-loop-iteration-limit declares
# `{"goals":{"max_turns":2}}` and IS the turn cap it asserts; without the merge
# the loop runs to the 20-turn default (cli.py:9914) and the cap is never
# reached, so the recording would capture a different scenario.
#
# The merge is deep (so a scenario setting one key under `goals` does not drop
# the rest of the file), the original is copied into the staging dir first, and
# the cleanup trap restores it on EVERY exit path including a `set -e` abort —
# a run must never leave the recording home mutated for the next cell.
#
# Inert for a settings-less cell: an empty/absent/`{}` blob returns before it
# touches anything, which is every other hermes cell today.
HERMES_CONFIG="${HERMES_HOME:-$HOME/.hermes}/config.yaml"
HERMES_CONFIG_BACKUP=""

apply_settings() {
  [[ -n "$SETTINGS_PATH" && -f "$SETTINGS_PATH" ]] || return 0
  local nkeys
  nkeys="$(jq -r 'if type == "object" then (keys | length) else 0 end' "$SETTINGS_PATH" 2>/dev/null || echo 0)"
  [[ "$nkeys" == "0" ]] && return 0
  [[ -f "$HERMES_CONFIG" ]] || {
    echo "[driver] recipe carries settings but $HERMES_CONFIG does not exist" >&2
    EXIT_REASON="nonzero(2)"; exit 1
  }
  # Back up BEFORE the first write, so restore_settings is armed even if the
  # merge itself fails halfway.
  HERMES_CONFIG_BACKUP="$STAGING/config.yaml.orig"
  cp "$HERMES_CONFIG" "$HERMES_CONFIG_BACKUP"
  python3 - "$HERMES_CONFIG" "$SETTINGS_PATH" <<'PY' || {
import json
import sys

import yaml

cfg_path, overlay_path = sys.argv[1], sys.argv[2]
with open(cfg_path) as fh:
    cfg = yaml.safe_load(fh) or {}
with open(overlay_path) as fh:
    overlay = json.load(fh)

def deep_merge(dst, src):
    for key, val in src.items():
        if isinstance(val, dict) and isinstance(dst.get(key), dict):
            deep_merge(dst[key], val)
        else:
            dst[key] = val

deep_merge(cfg, overlay)
with open(cfg_path, "w") as fh:
    yaml.safe_dump(cfg, fh, sort_keys=False, allow_unicode=True)
PY
    echo "[driver] failed to merge scenario settings into $HERMES_CONFIG" >&2
    EXIT_REASON="nonzero(2)"; exit 1
  }
  echo "[driver] merged scenario settings into $HERMES_CONFIG (restored on exit)" >&2
}

restore_settings() {
  [[ -n "$HERMES_CONFIG_BACKUP" && -f "$HERMES_CONFIG_BACKUP" ]] || return 0
  cp "$HERMES_CONFIG_BACKUP" "$HERMES_CONFIG" 2>/dev/null || true
}

# --- AGENT-SPECIFIC SEAM 1: launch the REPL under tmux -----------------------
# Port from the reference driver. Start the agent in a detached tmux session in
# $RUN_CWD, capturing stdout/stderr to "$DRIVER_LOG.stdout|.stderr". Pass the
# preferred UUID if the agent accepts one. The cleanup trap above tears it down.
launch_repl() {
  command -v tmux >/dev/null 2>&1 || { echo "[driver] tmux required" >&2; EXIT_REASON="nonzero(2)"; exit 1; }
  # alloc_slot mints a fresh slot, points SESSION at its tmux name and ACTIVE at
  # it, and clears the slot's TRANSCRIPT/UUID. restart/start_session call it again
  # to open another session; per-slot stdout (.stdout.$ACTIVE) feeds the contract.
  alloc_slot "hermesdrv-$$-$(date +%s)-$((N_SLOTS + 1))" "$RUN_CWD"
  # Epoch seconds, matching sessions.started_at (REAL seconds). Captured
  # BEFORE launch so the row this run creates always sorts after it.
  SES_LAUNCH_TS[$ACTIVE]="$(date +%s)"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  # `|| { … exit … }` keeps a launch failure from aborting under set -e WITHOUT
  # an accurate exit-reason — the cleanup trap then records nonzero(2).
  # `hermes chat` is the interactive REPL. HERMES_HOME must already point at
  # the recording home (the rig exports it) so this never writes into a real
  # session store.
  #
  # IRRLICHT_BIND_ADDR is passed per pane because hermes' hooks are
  # beacon-delivered (#1722): the `hooks:` entry irrlicht installs carries no
  # address at all and `irrlichd hook-post hermes` resolves one at FIRE time
  # from its own environment — IRRLICHT_BIND_ADDR first, then the addr file
  # under IRRLICHT_HOME (core/pkg/daemonaddr resolveClient). The beacon is a
  # descendant of this pane, and a tmux pane inherits the tmux SERVER's
  # environment rather than anything run-cell.sh exported, so a pane carrying
  # neither variable reads the PRODUCTION addr file and posts every hook to the
  # daemon on 7837. The recording then comes back complete, healthy and
  # hook-free with nothing anywhere saying why — the failure #1735 measured.
  # HERMES_HOME is the isolation half, for the same inheritance reason: the rig
  # exports it (lib/agent-home.sh's opt-in hermes row) so the DAEMON resolves
  # the scratch store, and without this prefix the pane's `hermes chat` would
  # write into the operator's real ~/.hermes while the daemon watched the
  # scratch one.
  #
  # Empty is the same as unset for both readers, so passing them unconditionally
  # is safe when the rig set neither.
  tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "${SES_CWD[$ACTIVE]}" \
    env "HERMES_HOME=${HERMES_HOME:-}" "IRRLICHT_BIND_ADDR=${IRRLICHT_BIND_ADDR:-}" "hermes chat" \
    >>"$DRIVER_LOG.stdout.$ACTIVE" 2>>"$DRIVER_LOG.stderr" \
    || { echo "[driver] failed to launch hermes under tmux" >&2; EXIT_REASON="nonzero(2)"; exit 1; }

  # SEAM 3: resolve the live session id. Hermes writes no transcript file —
  # the "transcript" IS the shared store — so the slot's transcript path is
  # "<store>?session=<id>", the same encoding the Go adapter's watcher puts on
  # agent.Event.TranscriptPath.
  #
  # The row is INSERTed on the first user message, ~2s after launch, so the
  # id does not exist yet at launch time. It is resolved lazily by
  # resolve_session_id after the first send.
  SES_TRANSCRIPT[$ACTIVE]=""
  SES_UUID[$ACTIVE]=""

  wait_for_prompt
}

# The TUI takes several seconds to boot (it loads 35 tools + 68 skills and
# renders a banner). Keystrokes sent before it finishes are swallowed with no
# error: the pane keeps showing an empty prompt and the driver then waits out
# its whole deadline for a turn that was never submitted. Poll for the ready
# prompt instead of sleeping a fixed guess.
wait_for_prompt() {
  local waited=0
  while (( waited < 60 )); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -q "Type your message or /help"; then
      # The banner is painted just before the input loop starts accepting.
      sleep 1
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "[driver] hermes REPL did not become ready within ${waited}s" >&2
  EXIT_REASON="nonzero(2)"
  return 1
}

# daemon_sid override. The shared lib derives the daemon's session id from a
# transcript FILENAME stem (basename minus .jsonl). Hermes has no transcript
# file — its slot path is "<store>?session=<id>" — so without this the
# staging contract reports "state.db?session=<id>" as the session id and
# nothing downstream matches the daemon's view.
daemon_sid() {
  local p="$1"
  [[ -z "$p" ]] && { echo ""; return; }
  case "$p" in
    *\?session=*) echo "${p##*\?session=}" ;;
    *) local b; b="$(basename "$p")"; echo "${b%.jsonl}" ;;
  esac
}

# hermes_store echoes the path of the store this run writes to.
hermes_store() { echo "${HERMES_HOME:-$HOME/.hermes}/state.db"; }

# hermes_sql runs one read-only query against the store. Returns empty on any
# error (store not created yet, writer holding the lock).
hermes_sql() { # <sql>
  local query="$1"
  sqlite3 "file:$(hermes_store)?mode=ro" "$query" 2>/dev/null || true
}

# The store is the ONLY session signal hermes exposes, so a missing sqlite3
# would surface as wait_turn silently spinning to the deadline. Fail loudly
# and immediately instead.
command -v sqlite3 >/dev/null 2>&1 || {
  echo "[driver] sqlite3 is required to read hermes' session store" >&2
  EXIT_REASON="nonzero(2)"; exit 2
}

# resolve_session_id binds the active slot to the newest TUI session started
# by this run. Called after the first send, once the row exists.
# The store is shared by every Hermes session on the machine, including ones
# this run did not launch, so "newest row" is NOT "our session" — a leftover
# CLI session from an earlier run would be picked up and its already-finished
# turn would satisfy wait_turn instantly (observed: a stale `source='cli'` row
# won the race and the driver reported a turn that never happened). Bind only
# to a session started AFTER this slot launched.
resolve_session_id() {
  [[ -n "${SES_UUID[$ACTIVE]:-}" ]] && return 0
  local id since="${SES_LAUNCH_TS[$ACTIVE]:-0}"
  id="$(hermes_sql "SELECT id FROM sessions WHERE source IN ('cli','tui') AND started_at > $since ORDER BY started_at DESC LIMIT 1;")"
  [[ -n "$id" ]] || return 1
  SES_UUID[$ACTIVE]="$id"
  SES_TRANSCRIPT[$ACTIVE]="$(hermes_store)?session=$id"
  return 0
}

# --- Store → JSONL export (the "transcript" hermes never writes) --------------
# Hermes persists NO transcript file, so the slot path used while driving is the
# pseudo-path "<store>?session=<id>" — the same encoding the watcher puts on
# agent.Event.TranscriptPath. That is not a file, and everything downstream of
# the driver wants one: curate-lifecycle-fixture.sh hard-exits "transcript not
# found", and without a transcript there is no replay golden, so `of verify`
# skips the whole observations block. So the run ends by materializing the
# store rows this session produced into a real JSONL, exactly the way the other
# store-backed column does (opencode's driver-interactive.sh).
#
# The row shape is dictated by core/adapters/inbound/agents/hermes/parser.go —
# keep this SELECT and metrics.go's foldMessages/scanMessageRow in lockstep:
#   * the SAME columns scanMessageRow injects (role, content, tool_calls,
#     tool_call_id, tool_name, finish_reason, display_kind) plus the synthetic
#     _ts / _model / _cwd the Parser reads for time, model and project binding;
#   * the SAME predicates metrics.go uses — active = 1 (a /rewind soft-deletes
#     by flipping it to 0, and replaying a rewound row would resurrect state the
#     user threw away) and ORDER BY timestamp ASC, id ASC;
#   * NULL columns coalesced to '' because scanMessageRow reads them through
#     sql.NullString, so the daemon sees '' and the fixture must not differ.
#
# tool_calls is emitted as a JSON *string*, not a nested object, and that is
# load-bearing: decodeToolCalls does `v.(string)` and then json.Unmarshal, so an
# embedded object would decode to nil and every tool call in the fixture would
# vanish. json_object() quotes a TEXT value, which is exactly the double
# encoding the column already carries.
#
# `timestamp` is emitted ALONGSIDE `_ts` and is not redundant. Two different
# readers want two different units from the same column:
#   * hermes' Parser reads `_ts` as epoch SECONDS (parser.go keyTimestamp);
#   * the replay engine's parseEventTimestamp (core/application/replayengine/
#     engine.go) reads `_ts` as MILLISECONDS — opencode's convention, since
#     opencode was the only store-backed column when it was written.
# Emitting seconds alone therefore built a correct Parser view on top of a
# virtual timeline in January 1970, which collapsed the golden's debounce
# behaviour (26 rows → 3 transitions instead of 7) and scaled every duration
# down by 1000x. parseEventTimestamp prefers a `timestamp` STRING in RFC3339
# and returns before it ever looks at `_ts`, and the Parser ignores keys it does
# not name — so giving the engine an ISO-8601 string and the Parser its seconds
# satisfies both without either lying.
export_transcript() { # <slot-index> — echoes the exported path
  # Two statements, deliberately: `local` is a builtin, so bash expands ALL of
  # its arguments before running it — a single `local slot="$1" sid=${SES_UUID[$slot]}`
  # evaluates the subscript while slot is still unset and aborts under `set -u`.
  local slot="$1"
  local sid="${SES_UUID[$slot]:-}"
  [[ -n "$sid" ]] || return 1
  local out="$STAGING/$sid.jsonl"
  : > "$out"
  # Read-only, like every other read this driver makes: the adapter's consent
  # copy promises "No row is ever written", and the export must not be the one
  # thing that breaks that promise on the maintainer's own store.
  sqlite3 "file:$(hermes_store)?mode=ro" >>"$out" <<SQL
.timeout 5000
.mode list
.headers off
.separator ""
SELECT json_object(
  'role',          m.role,
  'content',       ifnull(m.content, ''),
  'tool_calls',    ifnull(m.tool_calls, ''),
  'tool_call_id',  ifnull(m.tool_call_id, ''),
  'tool_name',     ifnull(m.tool_name, ''),
  'finish_reason', ifnull(m.finish_reason, ''),
  'display_kind',  ifnull(m.display_kind, ''),
  'timestamp',     strftime('%Y-%m-%dT%H:%M:%fZ', m.timestamp, 'unixepoch'),
  '_ts',           ifnull(m.timestamp, 0),
  '_model',        ifnull(s.model, ''),
  '_cwd',          ifnull(s.cwd, '')
)
FROM messages m
JOIN sessions s ON s.id = m.session_id
WHERE m.session_id = '$sid' AND m.active = 1
ORDER BY m.timestamp ASC, m.id ASC;
SQL
  [[ -s "$out" ]] || return 1
  echo "$out"
}

# export_all_transcripts rewrites every slot's path from the "<store>?session=<id>"
# pseudo-path to the exported JSONL. daemon_sid keeps reporting the same id
# either way: its `?session=` arm handles the pseudo-path and the file is NAMED
# <session-id>.jsonl, so the basename-stem fallback lands on the identical value.
export_all_transcripts() {
  local i path
  for (( i = 1; i <= N_SLOTS; i++ )); do
    if path="$(export_transcript "$i")"; then
      SES_TRANSCRIPT[$i]="$path"
      echo "[driver] exported slot $i (${SES_UUID[$i]}) -> $path ($(wc -l <"$path" | tr -d ' ') rows)" >&2
    else
      echo "[driver] slot $i has no exportable store rows (session='${SES_UUID[$i]:-}')" >&2
    fi
  done
}

# --- AGENT-SPECIFIC SEAM 2: detect a completed turn --------------------------
# Port the agent's turn-done signal: claudecode polls the transcript for
# stop_reason=="end_turn"; codex polls the rollout for task_complete; opencode
# polls the SQLite store for a step-finish. Return 0 when a NEW turn completed
# (or times out via remaining_seconds()).
# Hermes' turn boundary is explicit: the newest assistant row carries
# finish_reason='stop' when the turn is over, and 'tool_calls' while work
# continues. That makes this a real signal poll, not an idle heuristic.
wait_turn() {
  local deadline_reached=0
  while :; do
    (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; deadline_reached=1; break; }
    resolve_session_id || { sleep 1; continue; }

    local done_count
    done_count="$(hermes_sql "SELECT COUNT(*) FROM messages WHERE session_id='${SES_UUID[$ACTIVE]}' AND role='assistant' AND finish_reason='stop';")"
    [[ -z "$done_count" ]] && done_count=0
    if (( done_count > EXPECTED_TURNS_SEEN )); then
      EXPECTED_TURNS_SEEN=$done_count
      return 0
    fi
    sleep 1
  done
  (( deadline_reached )) && return 1
  return 0
}

# EXPECTED_TURNS_SEEN counts turns already observed, so wait_turn blocks for a
# NEW completion rather than returning immediately on the previous one.
EXPECTED_TURNS_SEEN=0

# --- AGENT-SPECIFIC SEAM 3: send text -----------------------------------------
# The REPL needs a beat between the literal text and Enter: sending them back
# to back races the TUI's input handling and the Enter is swallowed, leaving
# the prompt populated but never submitted (observed — the driver then sat at
# the prompt until its deadline while the pane showed the typed text).
send_text() { # <text>
  tmux send-keys -t "$SESSION" -l "$1"
  sleep 0.4
  tmux send-keys -t "$SESSION" Enter
}

# --- Step dispatch: ALL standard arms present; stubs fail loudly -------------
# Settings must land in config.yaml BEFORE the REPL boots — hermes reads its
# config once at startup, so a merge after launch_repl would be invisible.
apply_settings
launch_repl
while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  case "$type" in
    send|slash)      send_text "$(jq -r '.text' <<<"$step")" ;;
    wait_turn)       wait_turn || break ;;
    sleep)           sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)       not_implemented interrupt || break ;;       # TODO(hermes): Escape/Ctrl-C the in-flight turn
    keys)            not_implemented keys || break ;;            # TODO(hermes): tmux send-keys raw sequence
    reset_session)   not_implemented reset_session || break ;;   # TODO(hermes): in-REPL /clear|/new → new id, SAME slot; re-resolve SES_TRANSCRIPT[$ACTIVE] (SEAM 3)
    restart)         not_implemented restart || break ;;         # TODO(hermes): save_active; alloc_slot <name> <new-cwd>; launch — new slot carries the new id
    resume)          not_implemented resume || break ;;          # TODO(hermes): relaunch same id+cwd (1 session, 2 PIDs) — reuse the active slot
    sigkill)         not_implemented sigkill || break ;;         # TODO(hermes): kill -9 the active slot's PID
    exit_clean)      not_implemented exit_clean || break ;;      # TODO(hermes): Ctrl-D graceful shutdown
    start_session)   not_implemented start_session || break ;;   # TODO(hermes): save_active; alloc_slot; launch a CONCURRENT session, keep the first alive
    session)         not_implemented session || break ;;         # TODO(hermes): save_active; load_slot N — switch the active slot
    *)               echo "[driver] unknown step type: $type" >&2; EXIT_REASON="nonzero(2)"; break ;;
  esac
  (( $(remaining_seconds) <= 0 )) && { EXIT_REASON="timeout"; break; }
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# Materialize the store rows into real transcript files BEFORE the contract is
# written — emit_session_contract copies SES_TRANSCRIPT verbatim into
# transcript.path / transcript.paths, so the swap has to happen first.
export_all_transcripts

# --- Write the staging contract (shared) -------------------------------------
# emit_session_contract writes session.uuid + transcript.path (slot 1) plus the
# multi-session session.uuids / transcript.paths lists from SES_TRANSCRIPT, and
# combines the per-slot stdout (_lib/drive/contracts.sh). It needs each slot's
# SES_TRANSCRIPT[$i] populated by SEAM 3; until that's ported the paths are empty
# — the contract SHAPE is already correct, the scaffold just records nothing
# useful yet. The primary id is the daemon's session_id — daemon_sid of the
# transcript path; switch to the first-line UUID if hermes keys on that (see
# drive-pi-interactive.sh). drive_exit maps EXIT_REASON → the process exit code.
emit_session_contract "$(daemon_sid "${SES_TRANSCRIPT[1]}")"
# The epilogue completed: EXIT_REASON is this run's real verdict, so cleanup()
# must record it as-is rather than rewrite it as an abort.
REACHED_EPILOGUE=1
drive_exit
