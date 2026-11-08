#!/usr/bin/env bash
# driver-interactive.sh (opencode-v2) — drive OpenCode 2.x through the HTTP API
# of an already-running `opencode serve --service`, executing a step-script.
#
# WHY AN API DRIVER (#2109). The 2.x service hosts every session; a client is
# optional. Headless `opencode run` auto-rejects permission asks and hung
# before creating a session in 3 of 9 attempts (#2104), and this host has no
# tmux. So this driver launches NOTHING: it creates sessions over the API at
# RUN_CWD, prompts them, answers their permission asks and interrupts them,
# and reads each turn's end from the service's store, read-only.
#
# THE SERVICE IS NOT OURS (service-api.sh's header has the rest). The driver
# refuses — before creating anything — when service.json is missing, when its
# pid is dead or not `serve --service`, when more than one service runs, when
# the service's own XDG_* environment differs from this run's (the daemon would
# watch another tree), when OPENCODE_DB is set, and when the plugin's beacon,
# which runs INSIDE the service, would post to a different daemon than this
# run's (it resolves $HOME/.local/share/irrlicht/irrlichd.addr from the
# service's environment, so the run must coexist with IRRLICHT_ONBOARD_HOME set
# to that directory). replaydata/agents/opencode-v2/prerequisites.md names the
# exports.
#
# STEP TYPES (DRIVE_ELICITS below is what recipe-lint holds a recipe to):
#   send             POST /api/session/{id}/prompt
#   wait_turn        wait for a NEW time_idle on the session's session_v2 row:
#                    the idle mark is captured before each send, and a turn is
#                    over only when time_idle moves past it — a non-empty check
#                    is wrong from turn 2 on
#   sleep            pause N seconds (bounded by the deadline)
#   interrupt        POST /api/session/{id}/interrupt
#   permission_reply wait for the session's pending permission ask, hold it
#                    {"hold_seconds": N}, then answer it {"decision": "once"}
#   seed_instruction write {"path","text"} into RUN_CWD (AGENTS.md-shaped)
# Sessions (#2109 P5b-1) — every session is a 1-based "slot", as in the tmux
# drivers' contract; the first is slot 1:
#   start_session    POST /api/session at the same location; the new slot
#                    becomes active and the previous one stays live
#   session          {"session": N} switches the active slot; ANY step may
#                    carry "session": N to switch before it runs
#   reset_session    a new session at the same location, then the previously
#                    active one is archived (PATCH {"archived": true}) — the
#                    API form of the TUI's /new
#   archive          PATCH the active session {"archived": true}
#   unarchive        PATCH the active session {"archived": false}
#   model            POST /api/session/{id}/model {"provider","model"}
#   revert           rewind the active session to before its Nth-last user
#                    message ({"turns": N}, default 1): POST revert/stage
#                    {messageID, files: true}, then POST revert/commit
#   background       POST /api/session/{id}/background — move the session's
#                    running backgroundable tools into background observation
#   await_tool       wait until the active session's tool {"tool": NAME} runs
#   await_child      wait for a child session of the active one — a
#                    session_v2 row whose parent_id is it — and, with
#                    {"tool": NAME}, until a tool NAME of that child is running
# Compaction, shell escape and queued input (#2109 P5b-2):
#   compact          POST /api/session/{id}/compact — a manual compaction; it
#                    ends with an idle row, so a wait_turn after it waits for
#                    that NEW idle (the mark is taken before the POST)
#   shell            POST /api/session/{id}/shell {"command"} — the user's
#                    shell escape, run by the service with no model turn; the
#                    step waits until the session's shell row leaves running.
#                    Only echo/date/pwd/true with plain words is sent
#                    (oc2_valid_shell_command)
#   mid_turn_send    POST /api/session/{id}/prompt {"text","delivery"} WHILE a
#                    turn runs ({"delivery": "queue"} by default, or "steer").
#                    Refused unless the store shows the turn still in flight
#                    (oc2_turn_in_flight): a send after the turn ended would
#                    record an ordinary next turn as the scenario
# The service window (#2109 Q1: approved for 3-4 and 1-2's service-exit
# variant only):
#   exit_clean       SIGTERM the service; sigkill — SIGKILL it. Both refuse
#                    unless the operator exported OC2_SERVICE_WINDOW=1, and
#                    re-check that the pid is still a `serve --service`.
#                    Every slot is exported BEFORE the signal, and that is its
#                    transcript: the store's writer is gone afterwards. The
#                    driver never starts a service; restoring it is the
#                    operator's, outside the run.
# `keys`, `live` and `restart` have no API form here; a recipe naming one is
# refused by recipe-lint before anything runs.
#
# Staging contract (identical across drivers): driver.log, driver.exit-reason
# (ok | timeout | nonzero(N)), session.uuid[s], transcript.path[s] — one line
# per slot, in slot order; session.uuid is slot 1. Each transcript is the
# slot's session_message rows exported read-only from the store (service-api.sh
# oc2_export_session), one opencode.Parser line each.
#
# Usage:
#   driver-interactive.sh <staging-dir> <session-uuid> <timeout-seconds> <settings-path> <script-json>

set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: driver-interactive.sh <staging> <uuid> <timeout-s> <settings-path> <script-json>" >&2
  exit 2
fi

STAGING="$1"
# shellcheck disable=SC2034  # positional arg $2 of the driver protocol (run-cell.sh); opencode mints its own ses_ ids
UUID="$2"
TIMEOUT_S="$3"
SETTINGS_PATH="$4"
SCRIPT_JSON="$5"

mkdir -p "$STAGING"
DRIVER_LOG="$STAGING/driver.log"
: > "$DRIVER_LOG"

_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=service-api.sh
source "$_HERE/service-api.sh"
# drive_exit: the rig's one EXIT_REASON → process-exit-code mapping.
# shellcheck source=/dev/null
source "$_HERE/../../_lib/drive/contracts.sh"

# recipe-lint contract (#508 #4): the step types this driver genuinely elicits,
# scraped from this file by tools/onboarding-factory/scripts/lib/recipe-lint.sh.
# shellcheck disable=SC2034  # scraped from this file's SOURCE by recipe-lint.sh, never expanded in shell
DRIVE_ELICITS="send wait_turn sleep interrupt permission_reply seed_instruction start_session session reset_session archive unarchive model revert background await_tool await_child compact shell mid_turn_send exit_clean sigkill"
# shellcheck disable=SC2034  # scraped from this file's SOURCE by recipe-lint.sh
DRIVE_SLASH_REQUIRES_STEP_TYPE=true

# The variables the rig-home row exports (lib/agent-home.sh's opencode-v2 row),
# which the SERVICE must carry with the same values: there is no pane to hand
# them to, so this is checked rather than passed. righome's
# TestEveryRigHomeRowsDriverPassesTheHomeThroughTmux reads this line.
# shellcheck disable=SC2034  # read by the sourced service-api.sh (oc2_preflight)
SERVICE_ENV_VARS="XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME"

RUN_CWD="${IRRLICHT_ONBOARD_CWD:-$STAGING/cwd}"
mkdir -p "$RUN_CWD"
RUN_CWD="$(cd "$RUN_CWD" && pwd -P)"   # canonical: the service stores the resolved directory
DEADLINE=$(( $(oc2_now) + TIMEOUT_S ))
# shellcheck disable=SC2034  # read by the sourced service-api.sh (oc2_api's per-request deadline)
OC2_DEADLINE="$DEADLINE"
EXIT_REASON="ok"
REACHED_EPILOGUE=0
NONZERO_2='nonzero(2)'
TIMEOUT_REASON='timeout'

# Session slots: SLOT_SID[n-1] / SLOT_IDLE[n-1] hold slot n's id and idle
# mark; SESSION_ID / IDLE_MARK are the ACTIVE slot's (ACTIVE is 1-based, 0
# while no session exists). Indexed arrays only — macOS bash 3.2 has no
# `declare -A`.
SLOT_SID=()
SLOT_IDLE=()
ACTIVE=0
SESSION_ID=""
IDLE_MARK=0           # the active session's time_idle before its last send
SERVICE_SIGNALLED=0   # 1 once a service-window step has signalled the service

# Readiness timings, read at call time. The defaults ARE the production
# timings; they are overridable only so the stub-service test need not spend
# them in real seconds (the hook-install-wait.sh knob convention).
OC2_PLUGIN_WAIT_S="${OC2_PLUGIN_WAIT_S:-30}"     # plugin-load poll ceiling
OC2_PLUGIN_RECHECK_S="${OC2_PLUGIN_RECHECK_S:-1}" # second look after a load line
OC2_SEED_SETTLE_S="${OC2_SEED_SETTLE_S:-2}"       # session creation → first prompt

remaining_seconds() { local now; now=$(oc2_now); (( now >= DEADLINE )) && echo 0 || echo $((DEADLINE - now)); }
log() { echo "[driver] $*" | tee -a "$DRIVER_LOG" >&2; }

# The staging contract is honoured on ANY exit. An abort that never reached the
# epilogue never formed a verdict, so its "ok" is rewritten as a driver fault
# (#1825) — the same two-halved guard as the template's cleanup().
# BEGIN cleanup
cleanup() {
  if [[ "$REACHED_EPILOGUE" != "1" && "$EXIT_REASON" == "ok" ]]; then
    EXIT_REASON="$NONZERO_2"
    echo "[driver] aborted before the epilogue — recording exit reason $EXIT_REASON, not ok" >&2
  fi
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
}
# END cleanup
trap cleanup EXIT

refuse_and_exit() { EXIT_REASON="$NONZERO_2"; exit 2; }

# --- Preflight: everything the rig must not change -------------------------
oc2_preflight || refuse_and_exit
# The service window's steps signal the shared service. Without the operator's
# opt-in they are refused here, before a session exists or a model turn runs —
# not when the step is reached, after the recipe has already used the service.
if [[ "${OC2_SERVICE_WINDOW:-}" != "1" ]] && jq -e 'any(.[]; .type == "exit_clean" or .type == "sigkill")' <<<"$SCRIPT_JSON" > /dev/null; then
  log "refusing: this recipe signals the service (exit_clean/sigkill) — it is shared, and only an operator-approved service window (OC2_SERVICE_WINDOW=1) may stop it"
  refuse_and_exit
fi
log "service pid $OC2_SVC_PID at $OC2_URL; store $(oc2_store); RUN_CWD $RUN_CWD"

DB="$(oc2_store)"
PLUGIN="$XDG_CONFIG_HOME/opencode/plugin/irrlicht.js"
SERVICE_LOG="$XDG_DATA_HOME/opencode/log/opencode.log"

# Per-cell config, never the service's global one: opencode reads a
# project-local opencode.json at the session's location, measured to apply to
# an API session on the shared service (#2109's triage probe: a project-local
# bash:allow ran a shell call with no ask).
if [[ -n "${SETTINGS_PATH:-}" && -f "$SETTINGS_PATH" ]] \
   && [[ "$(jq -r 'if (type=="object" and (.|length)>0) then "yes" else "no" end' "$SETTINGS_PATH" 2>/dev/null)" == "yes" ]]; then
  jq '. + (if has("$schema") then {} else {"$schema":"https://opencode.ai/config.json"} end)' \
    "$SETTINGS_PATH" > "$RUN_CWD/opencode.json"
  log "seeded project config $RUN_CWD/opencode.json from recipe settings"
fi

# A recipe's path is confined to RUN_CWD: relative, non-empty, and with no
# `..` component (#2109 review F3).
step_seed_instruction() { # <path> <text>
  if [[ -z "$1" || "$1" == /* || "/$1/" == */../* ]]; then
    log "seed_instruction: refusing path '$1' — a recipe writes only under RUN_CWD, so the path must be relative with no '..' component"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  # Checked explicitly: both call sites sit in `||` lists, where errexit is
  # off inside this function, so an unchecked failed write would fall through
  # to the log line and record a run without its instruction as ok.
  if ! printf '%s' "$2" > "$RUN_CWD/$1"; then
    log "seed_instruction: could not write $RUN_CWD/$1 — refusing to record without the recipe's instruction"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "seed_instruction: wrote $(printf '%s' "$2" | wc -c | tr -d ' ') bytes to $RUN_CWD/$1"
}

# Leading seed_instruction steps run before the session exists, so the
# service's location instance reads the file when it boots.
while IFS= read -r _leading_step; do
  [[ "$(jq -r '.type' <<<"$_leading_step")" == "seed_instruction" ]] || break
  step_seed_instruction "$(jq -r '.path' <<<"$_leading_step")" "$(jq -r '.text' <<<"$_leading_step")" || refuse_and_exit
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Session + plugin readiness --------------------------------------------
# create_session opens a session at RUN_CWD and boots the service's location
# instance for it, which loads the plugin the daemon installed; the log's byte
# size taken first scopes the readiness check to this run's load.
#
# Creating a session does NOT boot its location — measured on the sandbox
# service (2.1.4): after POST /api/session the log showed only the project's
# git probes and no "location services booted" for 30s, so no plugin load
# either. The first location-scoped request boots it; GET /api/command for
# the location is the cheapest one, it changes nothing, and the boot it caused
# was logged within the same second.
create_session() {
  local mark waited=0 rc
  mark="$(wc -c < "$SERVICE_LOG" 2>/dev/null | tr -d ' ' || echo 0)"
  [[ "$mark" =~ ^[0-9]+$ ]] || mark=0
  new_session || return 1
  if ! oc2_api GET "/api/command?location%5Bdirectory%5D=$(jq -rn --arg d "$RUN_CWD" '$d|@uri')" > /dev/null; then
    log "booting the service's location for $RUN_CWD failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi

  # Readiness: the plugin loaded for this location with no failure, polled to
  # a deadline (OC2_PLUGIN_WAIT_S, or what is left of the run). A run that
  # drove before the plugin loaded would record a turn with no hook signal.
  while :; do
    rc=0; oc2_plugin_loaded "$SERVICE_LOG" "$mark" "$PLUGIN" || rc=$?
    if (( rc == 2 )); then
      log "the service logged 'failed to load plugin' for $PLUGIN — the installed artifact is not one this service loads; refusing to record a hook-free fixture"
      EXIT_REASON="$NONZERO_2"; return 1
    fi
    if (( rc == 0 )); then
      # One more look after the load line: a failing load logs its failure
      # within milliseconds of the attempt.
      sleep "$OC2_PLUGIN_RECHECK_S"
      rc=0; oc2_plugin_loaded "$SERVICE_LOG" "$mark" "$PLUGIN" || rc=$?
      (( rc == 0 )) && break
      continue
    fi
    if (( waited >= OC2_PLUGIN_WAIT_S || $(remaining_seconds) <= 0 )); then
      log "the plugin at $PLUGIN never logged a load in $SERVICE_LOG after ${waited}s — refusing to drive a session no hook can report"
      EXIT_REASON="$NONZERO_2"; return 1
    fi
    sleep 1; waited=$((waited + 1))
  done
  log "plugin loaded for $SESSION_ID after ${waited}s ($PLUGIN)"
  settle_new_session
}

# new_session opens one more session at RUN_CWD and makes it the active slot
# (the previous active slot is saved, and stays live). The location is booted
# once, by create_session; a later session at the same location joins it.
NEW_SESSION_AT=0
new_session() {
  local body sid
  body="$(jq -nc --arg d "$RUN_CWD" '{location:{directory:$d}}')"
  sid="$(oc2_api POST /api/session "$body" | jq -r '(.data // .).id // empty')" || true
  if ! oc2_valid_sid "$sid"; then
    log "the service did not create a session (got '$sid')"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  NEW_SESSION_AT="$(oc2_now)"
  save_active
  SLOT_SID+=("$sid"); SLOT_IDLE+=(0)
  load_slot "${#SLOT_SID[@]}"
  log "session: $sid (slot $ACTIVE)"
}

# The daemon's 2.x reader seeds a session's cursor the FIRST time it sees it
# and reads only later rows as activity (watcher_v2.go scanV2Session), and it
# scans at most once per 500ms (watcher.go defaultMinScanGap). Letting
# OC2_SEED_SETTLE_S (2s) pass between a session's creation and its first
# prompt is intended to keep the first turn out of that seed; it is a bound,
# not an observation — the daemon exposes no signal for "seeded".
settle_new_session() {
  while (( $(oc2_now) - NEW_SESSION_AT < OC2_SEED_SETTLE_S )); do sleep 0.5; done
  return 0
}

save_active() { # writes the active slot's idle mark back to its slot
  (( ACTIVE > 0 )) || return 0
  SLOT_IDLE[ACTIVE-1]="$IDLE_MARK"
  return 0
}
load_slot() { # <n> — makes slot n (1-based, existing) the active one
  ACTIVE="$1"; SESSION_ID="${SLOT_SID[ACTIVE-1]}"; IDLE_MARK="${SLOT_IDLE[ACTIVE-1]}"
  return 0
}

# switch_slot <n> switches the active session to slot n. Only a slot that
# exists is accepted: an unknown slot is a recipe bug, and driving the wrong
# session would record a different scenario.
switch_slot() {
  local n="$1"
  if [[ ! "$n" =~ ^[1-9][0-9]*$ ]] || (( n > ${#SLOT_SID[@]} )); then
    log "invalid session slot '$n' (have ${#SLOT_SID[@]}) — refusing to drive a session the recipe did not create"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  (( n == ACTIVE )) && return 0
  save_active
  load_slot "$n"
  log "switch -> session slot $n ($SESSION_ID)"
}

step_start_session() {
  new_session || return 1
  settle_new_session
}

# step_set_archived <true|false> [<sid>] archives or unarchives a session
# (the active one by default). The id came from the service and passed
# oc2_valid_sid when its slot was made, so it is safe in the URL path.
step_set_archived() {
  local flag="$1" sid="${2:-$SESSION_ID}"
  if ! oc2_api PATCH "/api/session/$sid" "$(jq -nc --argjson a "$flag" '{archived:$a}')" > /dev/null; then
    log "setting archived=$flag on $sid failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "archived=$flag on $sid"
}

step_reset_session() {
  local old="$SESSION_ID"
  new_session || return 1
  step_set_archived true "$old" || return 1
  settle_new_session
}

# step_model <provider> <model> switches the active session's model. Both go
# into a JSON body (built by jq, so quoting is not the risk); they are still
# held to a model id's shape, so a recipe typo is refused rather than sent.
step_model() {
  local provider="$1" model="$2"
  if ! oc2_valid_model_ref "$provider" "$model"; then
    log "model: refusing provider '$provider' / model '$model' — not a provider/model id"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  if ! oc2_api POST "/api/session/$SESSION_ID/model" "$(jq -nc --arg p "$provider" --arg m "$model" '{model:{id:$m, providerID:$p}}')" > /dev/null; then
    log "switching $SESSION_ID to $provider/$model failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "model of $SESSION_ID -> $provider/$model"
}

# step_revert <turns> rewinds the active session to before its Nth-last user
# message. The message id is read from the store (read-only) and goes into a
# request body; it must be a message id (oc2_valid_message_id).
step_revert() {
  local turns="$1" mid
  if [[ ! "$turns" =~ ^[1-9][0-9]*$ ]]; then
    log "revert: turns must be a positive integer, got '$turns'"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  mid="$(oc2_nth_last_user_message "$DB" "$SESSION_ID" "$turns")" || { EXIT_REASON="$NONZERO_2"; return 1; }
  if ! oc2_valid_message_id "$mid"; then
    log "revert: refusing '$mid' — not a message id (msg_…)"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  if ! oc2_api POST "/api/session/$SESSION_ID/revert/stage" "$(jq -nc --arg m "$mid" '{messageID:$m, files:true}')" > /dev/null \
     || ! oc2_api POST "/api/session/$SESSION_ID/revert/commit" > /dev/null; then
    log "revert of $SESSION_ID to before $mid failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "reverted $SESSION_ID to before $mid ($turns user turn(s))"
}

step_background() {
  if ! oc2_api POST "/api/session/$SESSION_ID/background" > /dev/null; then
    log "backgrounding $SESSION_ID's tools failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "background sent to $SESSION_ID"
}

# await_running_tool <sid> <tool> <label> polls, to the run's deadline, until a
# tool <tool> of session <sid> is running (oc2_running_tool) — the moment a
# recipe acts on a running tool, observed rather than slept for. <label> names
# what is awaited in the log.
await_running_tool() {
  local sid="$1" tool="$2" label="$3" state
  while :; do
    state="$(oc2_running_tool "$DB" "$sid" "$tool")" || { EXIT_REASON="$NONZERO_2"; return 1; }
    if [[ -n "$state" ]]; then
      log "$label is running its $tool tool"; return 0
    fi
    if (( $(remaining_seconds) <= 0 )); then
      log "$label ran no $tool tool by the deadline"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 1
  done
}

step_await_tool() { # <tool> — a tool of the active session
  if [[ -z "$1" ]]; then
    log "await_tool needs {\"tool\": NAME}"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  await_running_tool "$SESSION_ID" "$1" "$SESSION_ID"
}

# step_await_child [<tool>] waits, to the run's deadline, for a child session
# of the active one, and with <tool> until one of that child's tools named
# <tool> is running.
step_await_child() {
  local tool="$1" child=""
  until [[ -n "$child" ]]; do
    child="$(oc2_child_session "$DB" "$SESSION_ID")" || { EXIT_REASON="$NONZERO_2"; return 1; }
    [[ -n "$child" ]] && break
    if (( $(remaining_seconds) <= 0 )); then
      log "await_child: no child of $SESSION_ID by the deadline"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 1
  done
  if ! oc2_valid_sid "$child"; then
    log "await_child: refusing child '$child' — not a session id"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  if [[ -z "$tool" ]]; then
    log "child session $child of $SESSION_ID is up"; return 0
  fi
  await_running_tool "$child" "$tool" "child session $child of $SESSION_ID"
}

# export_slots [<note>] writes every slot's transcript (slot order) and lists
# them. Used by the epilogue, and by a service-window step right BEFORE its
# signal: after it the store's writer is gone, so what the store held then is
# the transcript.
export_slots() {
  local n sid out ok=0
  : > "$STAGING/transcript.paths"
  for (( n = 1; n <= ${#SLOT_SID[@]}; n++ )); do
    sid="${SLOT_SID[n-1]}"
    if (( n == 1 )); then out="$STAGING/opencode-v2-transcript.jsonl"; else out="$STAGING/opencode-v2-transcript-$n.jsonl"; fi
    if ! oc2_export_session "$DB" "$sid" "$out"; then
      log "export of $sid from $DB failed"; ok=1
    fi
    echo "$out" >> "$STAGING/transcript.paths"
    log "$sid -> $out ($(wc -l < "$out" | tr -d ' ') rows)${1:+ — $1}"
  done
  return "$ok"
}

# step_signal_service <SIGTERM|SIGKILL> — the service window's two steps.
step_signal_service() {
  local sig="$1" args signalled_at
  # The window opt-in was checked before the session existed (after preflight).
  args="$(ps -o args= -p "$OC2_SVC_PID" 2>/dev/null || true)"
  # shellcheck disable=SC2086  # word-splitting the argv string is the point
  if ! oc2_is_service_args $args; then
    log "$sig: refusing — pid $OC2_SVC_PID is no longer an opencode 'serve --service' (args: $args)"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  export_slots "exported before $sig" || { EXIT_REASON="$NONZERO_2"; return 1; }
  if ! kill -s "${sig#SIG}" "$OC2_SVC_PID"; then
    log "$sig to service pid $OC2_SVC_PID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  SERVICE_SIGNALLED=1
  signalled_at="$(oc2_now)"
  log "$sig sent to service pid $OC2_SVC_PID"
  while kill -0 "$OC2_SVC_PID" 2>/dev/null; do
    if (( $(remaining_seconds) <= 0 )); then
      log "$sig: service pid $OC2_SVC_PID still alive at the deadline ($(( $(oc2_now) - signalled_at ))s after the signal)"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 0.5
  done
  log "service pid $OC2_SVC_PID is gone after $(( $(oc2_now) - signalled_at ))s"
}

current_idle() { # prints the active session's time_idle (0 when never idle)
  local m
  m="$(oc2_idle_mark "$DB" "$SESSION_ID")" || return 1
  printf '%s\n' "${m%%|*}"
}

step_send() { # <text>
  local body
  IDLE_MARK="$(current_idle)" || { EXIT_REASON="$NONZERO_2"; return 1; }
  body="$(jq -nc --arg t "$1" '{text:$t}')"
  if ! oc2_api POST "/api/session/$SESSION_ID/prompt" "$body" > /dev/null; then
    log "prompt to $SESSION_ID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "send ($(printf '%s' "$1" | wc -c | tr -d ' ') chars) to $SESSION_ID; idle mark $IDLE_MARK"
}

step_wait_turn() {
  local m t outcome
  while :; do
    m="$(oc2_idle_mark "$DB" "$SESSION_ID")" || { EXIT_REASON="$NONZERO_2"; return 1; }
    t="${m%%|*}"; outcome="${m#*|}"
    if (( t > IDLE_MARK )) && [[ -n "$outcome" ]]; then
      log "turn over on $SESSION_ID: idle_outcome=$outcome (time_idle $t > mark $IDLE_MARK)"
      IDLE_MARK="$t"
      return 0
    fi
    if (( $(remaining_seconds) <= 0 )); then
      log "wait_turn: no new idle on $SESSION_ID by the deadline (mark $IDLE_MARK, now '$m')"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 1
  done
}

# step_compact asks the service for a manual compaction of the active session.
# The idle mark is taken first: the compaction ends with its own idle row
# (measured on the sandbox 2.1.4 service: a `compaction` row, status completed,
# reason manual, then an idle row, outcome succeeded).
step_compact() {
  IDLE_MARK="$(current_idle)" || { EXIT_REASON="$NONZERO_2"; return 1; }
  if ! oc2_api POST "/api/session/$SESSION_ID/compact" '{}' > /dev/null; then
    log "compaction of $SESSION_ID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "compact sent to $SESSION_ID; idle mark $IDLE_MARK"
}

# step_shell <command> runs the user's shell escape in the active session and
# waits, to the run's deadline, until the session has one more shell row out
# of `running` than before — observed, not slept for.
step_shell() {
  local cmd="$1" before now
  if ! oc2_valid_shell_command "$cmd"; then
    log "shell: refusing '$cmd' — only echo, date, pwd or true with plain words is run inside the shared service"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  before="$(oc2_shell_rows_done "$DB" "$SESSION_ID")" || { EXIT_REASON="$NONZERO_2"; return 1; }
  if ! oc2_api POST "/api/session/$SESSION_ID/shell" "$(jq -nc --arg c "$cmd" '{command:$c}')" > /dev/null; then
    log "shell escape on $SESSION_ID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  while :; do
    now="$(oc2_shell_rows_done "$DB" "$SESSION_ID")" || { EXIT_REASON="$NONZERO_2"; return 1; }
    if (( now > before )); then
      log "shell escape '$cmd' on $SESSION_ID finished ($now shell row(s) done)"; return 0
    fi
    if (( $(remaining_seconds) <= 0 )); then
      log "shell: '$cmd' on $SESSION_ID never left running by the deadline"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 1
  done
}

# step_mid_turn_send <text> <delivery> sends a prompt while the active
# session's turn runs. The delivery is the API's own enum (2.1.4 openapi
# Session.Inbox.Delivery: steer, queue). The idle mark is NOT moved: the turn
# the message joins is still the one wait_turn waits for.
step_mid_turn_send() {
  local text="$1" delivery="$2" running
  if [[ "$delivery" != "queue" && "$delivery" != "steer" ]]; then
    log "mid_turn_send: refusing delivery '$delivery' — the service takes queue or steer"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  if [[ -z "$text" ]]; then
    log "mid_turn_send needs {\"text\": …}"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  running="$(oc2_turn_in_flight "$DB" "$SESSION_ID")" || { EXIT_REASON="$NONZERO_2"; return 1; }
  if [[ "$running" != "yes" ]]; then
    log "mid_turn_send: refusing — no turn of $SESSION_ID is in flight, so this would be an ordinary next prompt, not a mid-turn one"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  if ! oc2_api POST "/api/session/$SESSION_ID/prompt" "$(jq -nc --arg t "$text" --arg d "$delivery" '{text:$t, delivery:$d}')" > /dev/null; then
    log "mid-turn prompt to $SESSION_ID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "mid_turn_send ($(printf '%s' "$text" | wc -c | tr -d ' ') chars, delivery $delivery) to $SESSION_ID; idle mark stays $IDLE_MARK"
}

step_interrupt() {
  if ! oc2_api POST "/api/session/$SESSION_ID/interrupt" > /dev/null; then
    log "interrupt of $SESSION_ID failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "interrupt sent to $SESSION_ID"
}

step_permission_reply() { # <decision> <hold-seconds>
  local decision="$1" hold="$2" rid=""
  while :; do
    rid="$(oc2_api GET "/api/session/$SESSION_ID/permission" | jq -r '((.data // .) | if type=="array" then .[0].id else empty end) // empty')" || rid=""
    [[ -n "$rid" ]] && break
    if (( $(remaining_seconds) <= 0 )); then
      log "permission_reply: no permission ask on $SESSION_ID by the deadline"
      EXIT_REASON="$TIMEOUT_REASON"; return 1
    fi
    sleep 1
  done
  # The id comes from the API and goes into the reply URL's path (#2109 review
  # F2): only a permission id's shape is answered.
  if ! oc2_valid_permission_id "$rid"; then
    log "permission_reply: refusing to answer '$rid' — not a permission id (per_…), and it would be put into the reply URL's path"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "permission ask $rid pending on $SESSION_ID; holding ${hold}s"
  bounded_sleep "$hold"
  if ! oc2_api POST "/api/session/$SESSION_ID/permission/$rid/reply" "$(jq -nc --arg d "$decision" '{decision:$d}')" > /dev/null; then
    log "reply to permission $rid failed"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  log "replied '$decision' to $rid"
}

bounded_sleep() { # <seconds> — never past the deadline
  local s="$1" left
  left="$(remaining_seconds)"
  (( s > left )) && s="$left"
  (( s > 0 )) && sleep "$s"
  return 0
}

create_session || exit 2

while IFS= read -r step; do
  type="$(jq -r '.type' <<<"$step")"
  # Optional inline target: switch to slot N before the step runs.
  tgt="$(jq -r '.session // empty' <<<"$step")"
  if [[ -n "$tgt" && "$type" != "start_session" ]]; then
    switch_slot "$tgt" || break
  fi
  case "$type" in
    send)             step_send "$(jq -r '.text' <<<"$step")" || break ;;
    wait_turn)        step_wait_turn || break ;;
    sleep)            bounded_sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)        step_interrupt || break ;;
    permission_reply) step_permission_reply "$(jq -r '.decision // "once"' <<<"$step")" "$(jq -r '.hold_seconds // 0' <<<"$step")" || break ;;
    seed_instruction) step_seed_instruction "$(jq -r '.path' <<<"$step")" "$(jq -r '.text' <<<"$step")" || break ;;
    start_session)    step_start_session || break ;;
    session)          [[ -n "$tgt" ]] || { log "a session step needs {\"session\": N}"; EXIT_REASON="$NONZERO_2"; break; } ;;
    reset_session)    step_reset_session || break ;;
    archive)          step_set_archived true || break ;;
    unarchive)        step_set_archived false || break ;;
    model)            step_model "$(jq -r '.provider // empty' <<<"$step")" "$(jq -r '.model // empty' <<<"$step")" || break ;;
    revert)           step_revert "$(jq -r '.turns // 1' <<<"$step")" || break ;;
    background)       step_background || break ;;
    await_tool)       step_await_tool "$(jq -r '.tool // empty' <<<"$step")" || break ;;
    await_child)      step_await_child "$(jq -r '.tool // empty' <<<"$step")" || break ;;
    compact)          step_compact || break ;;
    shell)            step_shell "$(jq -r '.command // empty' <<<"$step")" || break ;;
    mid_turn_send)    step_mid_turn_send "$(jq -r '.text // empty' <<<"$step")" "$(jq -r '.delivery // "queue"' <<<"$step")" || break ;;
    exit_clean)       step_signal_service SIGTERM || break ;;
    sigkill)          step_signal_service SIGKILL || break ;;
    *)                log "unknown step type: $type"; EXIT_REASON="$NONZERO_2"; break ;;
  esac
  if [[ "$EXIT_REASON" == "ok" ]] && (( $(remaining_seconds) <= 0 )); then
    EXIT_REASON="$TIMEOUT_REASON"; break
  fi
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Epilogue: export the session, write the contract ----------------------
# A signalled service already had every slot exported right before its signal
# (step_signal_service sets SERVICE_SIGNALLED only after that export); its
# store's writer is gone, so that export stands.
if (( SERVICE_SIGNALLED == 0 )); then
  if ! export_slots; then
    [[ "$EXIT_REASON" == "ok" ]] && EXIT_REASON="$NONZERO_2"
  fi
fi
printf '%s\n' "${SLOT_SID[@]}" > "$STAGING/session.uuids"
head -n1 "$STAGING/session.uuids" > "$STAGING/session.uuid"
head -n1 "$STAGING/transcript.paths" > "$STAGING/transcript.path"
log "${#SLOT_SID[@]} session(s): $(tr '\n' ' ' < "$STAGING/session.uuids"); exit reason: $EXIT_REASON"

REACHED_EPILOGUE=1
echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
drive_exit
