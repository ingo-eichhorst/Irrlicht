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
# `keys`, `live`, `restart`, `sigkill` have no API form here (there is no
# per-session process to signal), and multi-session steps are not ported yet;
# a recipe naming one is refused by recipe-lint before anything runs.
#
# Staging contract (identical across drivers): driver.log, driver.exit-reason
# (ok | timeout | nonzero(N)), session.uuid[s], transcript.path[s]. The
# transcript is the session's session_message rows exported read-only from the
# store (service-api.sh oc2_export_session), one opencode.Parser line each.
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
DRIVE_ELICITS="send wait_turn sleep interrupt permission_reply seed_instruction"
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

# The run's one session (multi-session steps are not ported yet).
SESSION_ID=""
IDLE_MARK=0   # the session's time_idle before its last send

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

step_seed_instruction() { # <path> <text>
  printf '%s' "$2" > "$RUN_CWD/$1"
  log "seed_instruction: wrote $(printf '%s' "$2" | wc -c | tr -d ' ') bytes to $RUN_CWD/$1"
}

# Leading seed_instruction steps run before the session exists, so the
# service's location instance reads the file when it boots.
while IFS= read -r _leading_step; do
  [[ "$(jq -r '.type' <<<"$_leading_step")" == "seed_instruction" ]] || break
  step_seed_instruction "$(jq -r '.path' <<<"$_leading_step")" "$(jq -r '.text' <<<"$_leading_step")"
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
  local mark body sid created waited=0 rc
  mark="$(wc -c < "$SERVICE_LOG" 2>/dev/null | tr -d ' ' || echo 0)"
  [[ "$mark" =~ ^[0-9]+$ ]] || mark=0
  body="$(jq -nc --arg d "$RUN_CWD" '{location:{directory:$d}}')"
  sid="$(oc2_api POST /api/session "$body" | jq -r '(.data // .).id // empty')" || true
  if ! oc2_valid_sid "$sid"; then
    log "the service did not create a session (got '$sid')"
    EXIT_REASON="$NONZERO_2"; return 1
  fi
  created="$(oc2_now)"
  SESSION_ID="$sid"
  log "session: $sid"
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
  log "plugin loaded for $sid after ${waited}s ($PLUGIN)"

  # The daemon's 2.x reader seeds a session's cursor the FIRST time it sees it
  # and reads only later rows as activity (watcher_v2.go scanV2Session), and it
  # scans at most once per 500ms (watcher.go defaultMinScanGap). Letting
  # OC2_SEED_SETTLE_S (2s) pass between the session's creation and its first
  # prompt is intended to keep the first turn out of that seed; it is a bound,
  # not an observation — the daemon exposes no signal for "seeded".
  while (( $(oc2_now) - created < OC2_SEED_SETTLE_S )); do sleep 0.5; done
  IDLE_MARK=0
  return 0
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
  case "$type" in
    send)             step_send "$(jq -r '.text' <<<"$step")" || break ;;
    wait_turn)        step_wait_turn || break ;;
    sleep)            bounded_sleep "$(jq -r '.seconds // 1' <<<"$step")" ;;
    interrupt)        step_interrupt || break ;;
    permission_reply) step_permission_reply "$(jq -r '.decision // "once"' <<<"$step")" "$(jq -r '.hold_seconds // 0' <<<"$step")" || break ;;
    seed_instruction) step_seed_instruction "$(jq -r '.path' <<<"$step")" "$(jq -r '.text' <<<"$step")" ;;
    *)                log "unknown step type: $type"; EXIT_REASON="$NONZERO_2"; break ;;
  esac
  if [[ "$EXIT_REASON" == "ok" ]] && (( $(remaining_seconds) <= 0 )); then
    EXIT_REASON="$TIMEOUT_REASON"; break
  fi
done < <(jq -c '.[]' <<<"$SCRIPT_JSON")

# --- Epilogue: export the session, write the contract ----------------------
TRANSCRIPT_OUT="$STAGING/opencode-v2-transcript.jsonl"
if ! oc2_export_session "$DB" "$SESSION_ID" "$TRANSCRIPT_OUT"; then
  log "export of $SESSION_ID from $DB failed"
  [[ "$EXIT_REASON" == "ok" ]] && EXIT_REASON="$NONZERO_2"
fi
echo "$SESSION_ID" > "$STAGING/session.uuid"
echo "$SESSION_ID" > "$STAGING/session.uuids"
echo "$TRANSCRIPT_OUT" > "$STAGING/transcript.path"
echo "$TRANSCRIPT_OUT" > "$STAGING/transcript.paths"
log "$SESSION_ID -> $TRANSCRIPT_OUT ($(wc -l < "$TRANSCRIPT_OUT" | tr -d ' ') rows); exit reason: $EXIT_REASON"

REACHED_EPILOGUE=1
echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
drive_exit
