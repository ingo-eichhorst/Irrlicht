#!/usr/bin/env bash
# run-cell-multi.sh — record a CROSS-ADAPTER cell: two (or more) different
# agents live in the SAME cwd at once, observed by ONE --record daemon.
# This is the recording rig for scenarios whose by_adapter cells declare a
# `partner_adapter` (e.g. multiple-agents-same-workspace), which the
# single-adapter run-cell.sh refuses.
#
# Pipeline:
#   precheck.sh (per adapter, coexist+multi)
#     → spawn ONE isolated `irrlichd --record` (own IRRLICHT_HOME + bind
#       port, so it COEXISTS with production — multi mode never stops it)
#     → launch each adapter's interactive driver CONCURRENTLY, all pointed
#       at one shared cwd via IRRLICHT_ONBOARD_CWD
#     → SIGINT → grace → drain the daemon
#     → for EACH adapter: curate a per-adapter fixture — that adapter's OWN
#       transcript, but an events.jsonl spanning the WHOLE workspace (the
#       other adapters' sessions unioned in via IRRLICHT_EXTRA_SESSION_IDS,
#       NOT their transcripts) so each fixture proves "two agents, one
#       workspace, labeled independently"
#     → replay each staged transcript
#     → write run-manifest.json
#
# claudecode observation: the isolated daemon is NOT on :7837, so
# claudecode's hooks (hardcoded to :7837) reach production, not us. That's
# fine — the daemon also observes claudecode via its transcript fswatcher
# (~/.claude/projects, keyed off the real $HOME), which is enough for this
# scenario's working->ready arcs. IRRLICHT_ONBOARD_MULTI=1 tells precheck
# to allow claudecode in coexist mode for exactly this reason.
#
# Coexist is MANDATORY here: IRRLICHT_ONBOARD_HOME must be set (defaulting
# the bind port to 7838) so we never touch the running production daemon.
#
# Usage:
#   IRRLICHT_ONBOARD_HOME=/tmp/irrlicht-onboard-dev \
#     run-cell-multi.sh <scenario-name>
#
# Outputs under ./.build/refresh/_multi/<scenario>-<UTC-ts>/:
#   recordings/                     — the single isolated daemon recording
#   cwd/                            — the shared workspace both agents run in
#   <adapter>/                      — per-adapter driver staging
#   replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl
#   reports/<adapter>.staged.json
#   daemon.log, run-manifest.json

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }
SCENARIOS_JSON="$SKILL_DIR/scenarios.json"

# Session-id reconciliation helpers (daemon_sid_for_transcript,
# sid_in_recording, reconcile_slot_csv) — shared + unit-tested in lib/.
# shellcheck source=lib/reconcile.sh
source "$SCRIPT_DIR/lib/reconcile.sh"
# Recipe ↔ driver lint (#476) — same backstop run-cell.sh applies, so the
# cross-adapter path also refuses a missing driver step BEFORE recording.
# shellcheck source=lib/recipe-lint.sh
source "$SCRIPT_DIR/lib/recipe-lint.sh"

[[ $# -eq 1 ]] || { echo "usage: run-cell-multi.sh <scenario-name>" >&2; exit 2; }
SCENARIO="$1"

# --- Coexist isolation is mandatory -------------------------------------
ONBOARD_HOME="${IRRLICHT_ONBOARD_HOME:-}"
if [[ -z "$ONBOARD_HOME" ]]; then
  echo "run-cell-multi requires IRRLICHT_ONBOARD_HOME (isolated daemon home) so it never disturbs production" >&2
  exit 2
fi
ONBOARD_BIND="${IRRLICHT_ONBOARD_BIND_ADDR:-127.0.0.1:7838}"
ONBOARD_SOCK="$ONBOARD_HOME/irrlichd.sock"
export IRRLICHT_ONBOARD_HOME="$ONBOARD_HOME"
export IRRLICHT_ONBOARD_BIND_ADDR="$ONBOARD_BIND"
export IRRLICHT_ONBOARD_MULTI=1

# --- Resolve the cross-adapter cell -------------------------------------
SCEN_JSON="$(jq -e --arg s "$SCENARIO" '.scenarios[] | select(.name==$s)' "$SCENARIOS_JSON")" \
  || { echo "scenario not found: $SCENARIO" >&2; exit 1; }
# (bash 3.2 — no mapfile; read into the array by hand.)
ADAPTERS=()
while IFS= read -r _a; do
  [[ -n "$_a" ]] && ADAPTERS+=("$_a")
done < <(jq -r '.cross_adapter[]?' <<<"$SCEN_JSON")
if [[ "${#ADAPTERS[@]}" -lt 2 ]]; then
  echo "scenario $SCENARIO has no cross_adapter[] list (need >= 2 adapters)" >&2
  exit 1
fi
echo "cross-adapter cell: $SCENARIO  adapters=[${ADAPTERS[*]}]"

# Each adapter must be applicable + carry a script.
for a in "${ADAPTERS[@]}"; do
  applic="$(jq -r --arg a "$a" 'if .by_adapter[$a].applicable==true then "true" else "false" end' <<<"$SCEN_JSON")"
  [[ "$applic" == "true" ]] || { echo "adapter $a is not applicable:true for $SCENARIO" >&2; exit 1; }
  has_script="$(jq -r --arg a "$a" '.by_adapter[$a].script | if (.|type)=="array" then "yes" else "no" end' <<<"$SCEN_JSON")"
  [[ "$has_script" == "yes" ]] || { echo "adapter $a has no script for $SCENARIO" >&2; exit 1; }
  # Driver-gap backstop (#476): refuse a step type this adapter's driver lacks
  # before launching any daemon/CLI, mirroring run-cell.sh's exit 3.
  if gaps="$(recipe_lint_gaps "$SCRIPT_DIR/drive-$a-interactive.sh" "$SCENARIOS_JSON" "$SCENARIO" "$a")"; then :; else
    echo "driver_gap: $a/$SCENARIO needs step type(s) drive-$a-interactive.sh doesn't implement:" >&2
    printf '  - gap:%s\n' $gaps >&2
    exit 3
  fi
done

# --- Precheck each adapter (builds bins, checks port, CLI versions) ------
for a in "${ADAPTERS[@]}"; do
  ATTACH=0 "$SCRIPT_DIR/precheck.sh" "$a" "$SCENARIOS_JSON"
done

DAEMON="$REPO_ROOT/.build/refresh/bin/irrlichd"
REPLAY_BIN="$REPO_ROOT/.build/refresh/bin/replay"

# --- Staging ------------------------------------------------------------
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/_multi/$SCENARIO-$TS"
SHARED_CWD="$STAGING/cwd"
mkdir -p "$STAGING/recordings" "$STAGING/reports" "$SHARED_CWD"

MANIFEST="$STAGING/run-manifest.json"
DAEMON_SHUTDOWN="unknown"

# write_error_manifest <error-code> [<extras-json>] — emit an ERROR-verdict
# run-manifest.json on a failure path so the implement skill's "read
# run-manifest.json → classify" step gets a verdict instead of finding no
# manifest (mirrors run-cell.sh's write_error_manifest contract). Includes
# each adapter driver's exit-reason (from its per-adapter staging subdir).
write_error_manifest() {
  local error_code="$1" a r
  local extras_json="${2:-}"
  [[ -n "$extras_json" ]] || extras_json="{}"
  local reasons="{}"
  for a in "${ADAPTERS[@]}"; do
    r="$(cat "$STAGING/$a/driver.exit-reason" 2>/dev/null || echo missing)"
    reasons="$(jq -n --argjson o "$reasons" --arg k "$a" --arg v "$r" '$o + {($k): $v}')"
  done
  jq -n \
    --arg scenario "$SCENARIO" \
    --argjson adapters "$(printf '%s\n' "${ADAPTERS[@]}" | jq -R . | jq -s .)" \
    --arg error "$error_code" \
    --arg staging "$STAGING" \
    --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
    --argjson driver_exit_reasons "$reasons" \
    --argjson extras "$extras_json" \
    '{scenario: $scenario, verdict: "ERROR", cross_adapter: $adapters,
      error: $error, staging: $staging, daemon_shutdown: $daemon_shutdown,
      driver_exit_reasons: $driver_exit_reasons} + $extras' \
    > "$MANIFEST"
}

# --- Spawn ONE isolated --record daemon ---------------------------------
DAEMON_LOG="$STAGING/daemon.log"
env IRRLICHT_RECORDINGS_DIR="$STAGING/recordings" \
  IRRLICHT_BIND_ADDR="$ONBOARD_BIND" \
  IRRLICHT_HOME="$ONBOARD_HOME" \
  "$DAEMON" --record >"$DAEMON_LOG" 2>&1 &
DAEMON_PID=$!
echo "daemon started (pid $DAEMON_PID, bind=$ONBOARD_BIND, home=$ONBOARD_HOME)"

SHUTDOWN_REASON="unknown"
cleanup() {
  if kill -0 "$DAEMON_PID" 2>/dev/null; then
    SHUTDOWN_REASON="sigint"
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 12); do
      kill -0 "$DAEMON_PID" 2>/dev/null || { echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"; return; }
      sleep 0.5
    done
    SHUTDOWN_REASON="sigterm"
    kill -TERM "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 6); do
      kill -0 "$DAEMON_PID" 2>/dev/null || { echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"; return; }
      sleep 0.5
    done
    SHUTDOWN_REASON="sigkill"
    kill -KILL "$DAEMON_PID" 2>/dev/null || true
  fi
  echo "$SHUTDOWN_REASON" > "$STAGING/daemon.shutdown"
}
trap cleanup EXIT

for _ in $(seq 1 40); do
  [[ -S "$ONBOARD_SOCK" ]] && break
  sleep 0.25
done
[[ -S "$ONBOARD_SOCK" ]] || { echo "daemon socket never appeared: $ONBOARD_SOCK" >&2; write_error_manifest "daemon_socket_missing"; exit 1; }

# --- Launch every adapter's interactive driver CONCURRENTLY -------------
# All share $SHARED_CWD (the same workspace). Each gets its own staging
# subdir, fresh preferred-UUID, settings.json, and the cell's script.
declare -a DRV_PIDS=() DRV_ADAPTERS=()
for a in "${ADAPTERS[@]}"; do
  sub="$STAGING/$a"
  mkdir -p "$sub"
  jq --arg a "$a" '.by_adapter[$a].settings // {}' <<<"$SCEN_JSON" > "$sub/settings.json"
  script_json="$(jq -c --arg a "$a" '.by_adapter[$a].script' <<<"$SCEN_JSON")"
  timeout_s="$(jq -r --arg a "$a" '.by_adapter[$a].timeout_seconds // 240' <<<"$SCEN_JSON")"
  uuid="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  driver="$SCRIPT_DIR/drive-$a-interactive.sh"
  [[ -x "$driver" ]] || { echo "driver missing: $driver" >&2; exit 1; }
  echo "launching $a driver (shared cwd=$SHARED_CWD, timeout=${timeout_s}s)"
  IRRLICHT_ONBOARD_CWD="$SHARED_CWD" \
    "$driver" "$sub" "$uuid" "$timeout_s" "$sub/settings.json" "$script_json" \
    >"$sub/driver.out" 2>&1 &
  DRV_PIDS+=($!)
  DRV_ADAPTERS+=("$a")
done

# Wait for all drivers; record each exit status.
DRV_FAIL=0
for i in "${!DRV_PIDS[@]}"; do
  if wait "${DRV_PIDS[$i]}"; then
    echo "driver ${DRV_ADAPTERS[$i]}: ok"
  else
    rc=$?
    echo "driver ${DRV_ADAPTERS[$i]}: FAILED (exit $rc)" >&2
    DRV_FAIL=1
  fi
done

# --- Drain the daemon ---------------------------------------------------
cleanup
trap - EXIT
DAEMON_SHUTDOWN="$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo unknown)"

# --- Locate the single recording ----------------------------------------
RECORDING="$(find "$STAGING/recordings" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | head -n1)"
[[ -n "$RECORDING" ]] || { echo "no recording produced under $STAGING/recordings" >&2; write_error_manifest "no_recording"; exit 1; }

# --- Collect each adapter's daemon session_id(s) + transcript(s) --------
# Drivers write session.uuid/transcript.path (the slot-1 PRIMARY, already
# the DAEMON-side session_id: rollout-stem for codex, UUID for claudecode)
# AND session.uuids/transcript.paths (ALL slots, in order) when a script
# chains start_session/reset_session/fork. We curate each adapter's fixture
# from its PRIMARY transcript but union EVERY session (all adapters, all
# slots) into the workspace events.jsonl. (bash 3.2 — parallel indexed
# arrays keyed by ADAPTERS position.)
# Reconciliation helpers (daemon_sid_for_transcript, sid_in_recording,
# reconcile_slot_csv) live in lib/reconcile.sh, sourced above and unit-tested
# by lib/reconcile_test.sh. They map each driver-written id to the
# daemon-recorded session_id and verify it actually appears in the recording.

PRIMARY_SID=()
PRIMARY_TRANSCRIPT=()
OWN_TRANSCRIPTS=()   # this adapter's own slot transcripts, newline-joined
ALL_SIDS=()          # flat union of every adapter's every slot sid
for idx in "${!ADAPTERS[@]}"; do
  a="${ADAPTERS[$idx]}"
  sub="$STAGING/$a"
  PRIMARY_TRANSCRIPT[$idx]="$(head -n1 "$sub/transcript.path" 2>/dev/null || true)"
  raw_primary_sid="$(head -n1 "$sub/session.uuid" 2>/dev/null || true)"
  # Reconcile the driver's preferred id to the daemon's recorded session_id.
  PRIMARY_SID[$idx]="$(daemon_sid_for_transcript "${PRIMARY_TRANSCRIPT[$idx]}" "$a" "$raw_primary_sid")"
  if [[ -z "${PRIMARY_SID[$idx]}" || -z "${PRIMARY_TRANSCRIPT[$idx]}" || ! -f "${PRIMARY_TRANSCRIPT[$idx]}" ]]; then
    echo "ERROR: $a driver did not resolve a session (sid=${PRIMARY_SID[$idx]:-missing}, transcript=${PRIMARY_TRANSCRIPT[$idx]:-missing})" >&2
    DRV_FAIL=1
    continue
  fi
  # The reconciled primary MUST be an id the daemon actually recorded. If
  # reconcile fell back to the driver-written id (transcript_path mismatch /
  # transcript unobserved) and that id never appears in the recording,
  # curating against it yields a silently-empty per-adapter arc — fail loudly
  # instead of staging a fixture that doesn't support its own assertions.
  if ! sid_in_recording "${PRIMARY_SID[$idx]}"; then
    echo "ERROR: $a primary session '${PRIMARY_SID[$idx]}' does not appear in the recording — reconcile fell back to a driver id the daemon never recorded; the per-adapter arc would be empty. Not staging." >&2
    DRV_FAIL=1
    continue
  fi
  # Full per-slot lists (fall back to the single-file primaries). Reconcile
  # each slot's id against its matching transcript path so every chained
  # session is filtered by its daemon-recorded id, not its preferred id.
  uuids_file="$sub/session.uuids"; [[ -f "$uuids_file" ]] || uuids_file="$sub/session.uuid"
  paths_file="$sub/transcript.paths"; [[ -f "$paths_file" ]] || paths_file="$sub/transcript.path"
  # Reconcile every slot's id against its matching transcript path (kept in
  # lockstep by reconcile_slot_csv) so each chained session is filtered by its
  # daemon-recorded id, not its driver-preferred id. The while loop runs in
  # the current shell (process substitution, not a pipe) so ALL_SIDS persists.
  csv=""
  while IFS= read -r sid; do
    csv+="${csv:+,}$sid"
    ALL_SIDS+=("$sid")
  done < <(reconcile_slot_csv "$uuids_file" "$paths_file" "$a")
  OWN_TRANSCRIPTS[$idx]="$(cat "$paths_file" 2>/dev/null || true)"
  echo "$a: primary=${PRIMARY_SID[$idx]} sids=[$csv]"
done
[[ "$DRV_FAIL" -eq 0 ]] || { echo "one or more drivers failed; not curating" >&2; write_error_manifest "driver_failed"; exit 1; }

# --- Curate one per-adapter fixture each --------------------------------
# events.jsonl spans the WHOLE workspace: every session of every adapter
# (ALL_SIDS) is unioned in via IRRLICHT_EXTRA_SESSION_IDS. transcript.jsonl
# stays THIS adapter's own — IRRLICHT_EXTRA_TRANSCRIPTS carries only this
# adapter's slot transcripts (concatenated if it chained sessions), never
# another adapter's (different format); for a single-slot adapter it's left
# empty so curate does a plain copy.
for idx in "${!ADAPTERS[@]}"; do
  a="${ADAPTERS[$idx]}"
  # extras = every sid except this adapter's primary (curate adds the
  # primary itself; its sort -u dedups any overlap with this adapter's
  # own extra slots).
  extra=""
  for s in ${ALL_SIDS[@]+"${ALL_SIDS[@]}"}; do
    [[ "$s" == "${PRIMARY_SID[$idx]}" ]] && continue
    extra+="${extra:+,}$s"
  done
  # Only concatenate this adapter's transcripts when it has more than one
  # slot; otherwise leave empty so curate copies the single primary.
  own_t=""
  if [[ "$(printf '%s\n' "${OWN_TRANSCRIPTS[$idx]}" | grep -c .)" -gt 1 ]]; then
    own_t="${OWN_TRANSCRIPTS[$idx]}"
  fi
  echo "curating $a (primary=${PRIMARY_SID[$idx]}, workspace extras: $extra)"
  IRRLICHT_EXTRA_SESSION_IDS="$extra" \
  IRRLICHT_EXTRA_TRANSCRIPTS="$own_t" \
    "$REPO_ROOT/tools/curate-lifecycle-fixture.sh" \
      -d "$STAGING/replaydata/agents" \
      "$RECORDING" "${PRIMARY_SID[$idx]}" "${PRIMARY_TRANSCRIPT[$idx]}" "$a" "$SCENARIO"

  ext="$(jq -r '.transcript_extension // "jsonl"' "$REPO_ROOT/replaydata/agents/$a/capabilities.json")"
  staged_t="$STAGING/replaydata/agents/$a/scenarios/$SCENARIO/transcript.$ext"
  (cd "$REPO_ROOT" && "$REPLAY_BIN" --quiet --out "$STAGING/reports/$a.staged.json" "$staged_t") || true
  [[ -s "$STAGING/reports/$a.staged.json" ]] || { echo "replay failed for $a ($staged_t)" >&2; write_error_manifest "replay_failed" "$(jq -nc --arg a "$a" '{failed_adapter:$a}')"; exit 1; }
done

# --- Manifest -----------------------------------------------------------
jq -n \
  --arg scenario "$SCENARIO" \
  --argjson adapters "$(printf '%s\n' "${ADAPTERS[@]}" | jq -R . | jq -s .)" \
  --argjson sids "$(printf '%s\n' "${ALL_SIDS[@]}" | jq -R . | jq -s .)" \
  --arg staging "$STAGING" \
  --arg raw_recording "$RECORDING" \
  --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
  '{scenario: $scenario,
    verdict: "STAGED",
    cross_adapter: $adapters,
    session_ids: $sids,
    staging: $staging,
    raw_recording: $raw_recording,
    daemon_shutdown: $daemon_shutdown}' \
  > "$MANIFEST"

echo "manifest: $MANIFEST"
echo "staged fixtures:"
for a in "${ADAPTERS[@]}"; do
  echo "  $STAGING/replaydata/agents/$a/scenarios/$SCENARIO/"
done
