#!/usr/bin/env bash
# run-cell.sh — execute one (adapter, scenario) cell end-to-end.
#
# Pipeline:
#   precheck.sh  →  spawn isolated irrlichd --record
#                →  drive-<adapter>.sh (runs the agent under timeout)
#                →  SIGINT → 6s grace → SIGTERM → SIGKILL the daemon
#                →  resolve transcript path from session UUID
#                →  tools/curate-lifecycle-fixture.sh -d <staging>/replaydata/agents
#                →  replay against staged + committed fixtures
#                →  write run-manifest.json
#
# After this script returns, the caller (skill.md, driven by Claude) reads
# the manifest + two replay reports and summarizes material changes.
#
# Usage:
#   run-cell.sh <adapter> <scenario-name>
#
# Outputs under ./.build/refresh/<adapter>/<scenario>-<UTC-ts>/:
#   recordings/            — isolated daemon recording (raw)
#   replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl  — staged fixture
#   reports/staged.json    — replay report over staged fixture
#   reports/committed.json — replay report over committed fixture (if any)
#   driver.log, driver.exit-reason, daemon.log
#   settings.json          — scenario's settings blob, written here for driver
#   run-manifest.json      — summary for the summarizer step

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKILL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$REPO_ROOT" ]] || { echo "not in a git repo" >&2; exit 1; }

SCENARIOS_JSON="$SKILL_DIR/scenarios.json"

if [[ $# -ne 2 ]]; then
  echo "usage: run-cell.sh <adapter> <scenario-name>" >&2
  exit 2
fi
ADAPTER="$1"
SCENARIO="$2"

# Look up the cell from scenarios.json. Absent cell → refuse.
# A cell carries either `prompt` (single-shot, headless driver) or `script`
# (array of step objects, interactive driver). Both can't be set.
CELL_JSON="$(jq --arg s "$SCENARIO" --arg a "$ADAPTER" '
  .scenarios[]
  | select(.name == $s)
  | select(.by_adapter[$a])
  | {
      description,
      requires,
      verify,
      prompt: .by_adapter[$a].prompt,
      script: .by_adapter[$a].script,
      settings: .by_adapter[$a].settings,
      timeout_seconds: .by_adapter[$a].timeout_seconds
    }
' "$SCENARIOS_JSON")"
if [[ -z "$CELL_JSON" || "$CELL_JSON" == "null" ]]; then
  echo "cell not found: scenario=$SCENARIO adapter=$ADAPTER (either unknown or missing-prompt)" >&2
  exit 1
fi

TIMEOUT_S="$(jq -r '.timeout_seconds' <<<"$CELL_JSON")"
PROMPT="$(jq -r '.prompt // ""' <<<"$CELL_JSON")"
SCRIPT_JSON="$(jq -c '.script // empty' <<<"$CELL_JSON")"
if [[ -z "$PROMPT" && -z "$SCRIPT_JSON" ]]; then
  echo "cell has neither prompt nor script: scenario=$SCENARIO adapter=$ADAPTER" >&2
  exit 1
fi

# --- Precheck ------------------------------------------------------------
"$SCRIPT_DIR/precheck.sh" "$ADAPTER" "$SCENARIOS_JSON"

# --- Staging -------------------------------------------------------------
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/$ADAPTER/$SCENARIO-$TS"
# shellcheck source=lib/assert-staging-path.sh
. "$SCRIPT_DIR/lib/assert-staging-path.sh"
mkdir -p "$STAGING/recordings" "$STAGING/replaydata/agents/$ADAPTER/scenarios/$SCENARIO" "$STAGING/reports"

# Scenario's settings blob → staging file, passed to driver as a path.
# This avoids --settings <json-blob> shell-quoting fragility.
jq '.settings' <<<"$CELL_JSON" > "$STAGING/settings.json"

UUID="$(uuidgen | tr '[:upper:]' '[:lower:]')"

DAEMON="$REPO_ROOT/.build/refresh/bin/irrlichd"
REPLAY_BIN="$REPO_ROOT/.build/refresh/bin/replay"

# --- Launch isolated daemon ---------------------------------------------
DAEMON_LOG="$STAGING/daemon.log"
IRRLICHT_RECORDINGS_DIR="$STAGING/recordings" \
  IRRLICHT_BIND_ADDR="127.0.0.1:7837" \
  "$DAEMON" --record >"$DAEMON_LOG" 2>&1 &
DAEMON_PID=$!
echo "daemon started (pid $DAEMON_PID)"

# Cleanup: graceful shutdown. Runs once: either via explicit call before
# transcript resolution (we must drain before continuing), or as the EXIT
# trap if we fail before reaching that point. `trap - EXIT` after the
# explicit call prevents double-invocation.
SHUTDOWN_REASON="unknown"
cleanup() {
  if kill -0 "$DAEMON_PID" 2>/dev/null; then
    SHUTDOWN_REASON="sigint"
    kill -INT "$DAEMON_PID" 2>/dev/null || true
    # 6s grace = 5s recorder flush interval + 1s slack.
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

# Wait up to 10s for the unix socket to appear — signals the daemon is
# ready to accept connections.
SOCK="$HOME/.local/share/irrlicht/irrlichd.sock"
for _ in $(seq 1 40); do
  [[ -S "$SOCK" ]] && break
  sleep 0.25
done
[[ -S "$SOCK" ]] || { echo "daemon socket never appeared: $SOCK" >&2; exit 1; }

# --- Drive the agent ----------------------------------------------------
# Drivers are responsible for resolving the transcript path and writing
# session.uuid + transcript.path back to staging. UUID arg $2 is a
# "preferred" UUID — drive-claudecode.sh honors it via --session-id;
# codex/pi drivers ignore it and surface the agent-assigned UUID.
# Cells with a `script` block route through the interactive driver (REPL +
# step-script). Plain `prompt` cells use the headless driver.
if [[ -n "$SCRIPT_JSON" ]]; then
  DRIVER="$SCRIPT_DIR/drive-$ADAPTER-interactive.sh"
  DRIVER_INPUT="$SCRIPT_JSON"
else
  DRIVER="$SCRIPT_DIR/drive-$ADAPTER.sh"
  DRIVER_INPUT="$PROMPT"
fi
[[ -x "$DRIVER" ]] || { echo "driver missing: $DRIVER" >&2; exit 1; }
set +e
"$DRIVER" "$STAGING" "$UUID" "$TIMEOUT_S" "$STAGING/settings.json" "$DRIVER_INPUT"
set -e
DRIVER_REASON="$(cat "$STAGING/driver.exit-reason" 2>/dev/null || echo "unknown")"

# Drain the daemon now — the recorder must flush before we curate.
cleanup
trap - EXIT

# --- Read driver-resolved transcript + actual UUID ----------------------
TRANSCRIPT="$(cat "$STAGING/transcript.path" 2>/dev/null || true)"
ACTUAL_UUID="$(cat "$STAGING/session.uuid" 2>/dev/null || true)"

# --- Locate the recording file ------------------------------------------
RECORDING="$(find "$STAGING/recordings" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | head -n1)"

# --- Aider session-id mapping -------------------------------------------
# Aider has no native session-id; the daemon synthesizes proc-<pid> per
# observed process. The driver wrote a synthesized UUID to session.uuid
# for fixture-naming parity — replace it with the actual proc-<pid> the
# daemon used so curate-lifecycle-fixture.sh can filter the recording
# against real events. When multiple PIDs share one transcript (Python
# wrapper + worker), we pick the earliest by sequence number; curate's
# existing pid_discovered scan picks up the other PIDs from there.
if [[ "$ADAPTER" == "aider" && -n "$RECORDING" && -n "$TRANSCRIPT" ]]; then
  AIDER_SID="$(jq -r --arg path "$TRANSCRIPT" '
    select(.adapter=="aider" and .kind=="transcript_new" and .transcript_path==$path)
    | [.seq, .session_id] | @tsv' "$RECORDING" | sort -n | head -n1 | cut -f2)"
  if [[ -n "$AIDER_SID" ]]; then
    ACTUAL_UUID="$AIDER_SID"
    echo "$ACTUAL_UUID" > "$STAGING/session.uuid"
  fi
fi

MANIFEST="$STAGING/run-manifest.json"
DAEMON_SHUTDOWN="$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo "unknown")"

# Write an ERROR-verdict run-manifest with the standard envelope plus
# error-specific fields supplied as a JSON object (pass '{}' for none).
write_error_manifest() {
  local error_code="$1"
  local extras_json="$2"
  jq -n \
    --arg adapter "$ADAPTER" \
    --arg scenario "$SCENARIO" \
    --arg session_uuid "$ACTUAL_UUID" \
    --arg error "$error_code" \
    --arg driver_exit_reason "$DRIVER_REASON" \
    --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
    --arg staging "$STAGING" \
    --argjson extras "$extras_json" \
    '{adapter: $adapter,
      scenario: $scenario,
      session_uuid: $session_uuid,
      verdict: "ERROR",
      error: $error,
      driver_exit_reason: $driver_exit_reason,
      daemon_shutdown: $daemon_shutdown,
      staging: $staging} + $extras' \
    > "$MANIFEST"
}

if [[ -z "$TRANSCRIPT" || -z "$RECORDING" || -z "$ACTUAL_UUID" ]]; then
  write_error_manifest "transcript_recording_or_uuid_missing" \
    "$(jq -nc \
        --argjson transcript_found "$([[ -n "$TRANSCRIPT" ]] && echo true || echo false)" \
        --argjson recording_found "$([[ -n "$RECORDING" ]] && echo true || echo false)" \
        --argjson uuid_resolved "$([[ -n "$ACTUAL_UUID" ]] && echo true || echo false)" \
        '{transcript_found: $transcript_found, recording_found: $recording_found, uuid_resolved: $uuid_resolved}')"
  echo "ERROR: transcript=${TRANSCRIPT:-missing} recording=${RECORDING:-missing} uuid=${ACTUAL_UUID:-missing}" >&2
  exit 1
fi

# --- Subagent probe -----------------------------------------------------
# If the scenario requires the `subagents` capability, the run is only
# meaningful if the parent actually emitted Agent tool calls and the daemon
# saw the resulting parent_linked events. Fail cleanly here so the manifest
# carries a structured reason instead of producing an empty .subagents/ dir
# downstream.
# "subagents" matches agents.CapSubagents in core/adapters/inbound/agents/config.go.
REQUIRES_SUBAGENTS="$(jq -r '.requires | index("subagents") // empty' <<<"$CELL_JSON")"
if [[ -n "$REQUIRES_SUBAGENTS" ]]; then
  PARENT_LINKED_COUNT="$(jq -c --arg sid "$ACTUAL_UUID" \
    'select(.kind=="parent_linked" and .parent_session_id==$sid)' \
    "$RECORDING" | wc -l | tr -d ' ')"
  SUBAGENT_DIR="$(dirname "$TRANSCRIPT")/$ACTUAL_UUID/subagents"
  count_subagent_files() {
    find "$SUBAGENT_DIR" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | wc -l | tr -d ' '
  }
  SUBAGENT_FILES="$(count_subagent_files)"
  # If the daemon saw parent_linked events but the child transcripts
  # haven't been flushed to disk yet (race against the parent transcript's
  # appearance), poll briefly. We only poll when we already know children
  # exist — otherwise there's nothing to wait for.
  if [[ "$PARENT_LINKED_COUNT" -gt 0 && "$SUBAGENT_FILES" -eq 0 ]]; then
    for _ in $(seq 1 20); do
      sleep 0.5
      SUBAGENT_FILES="$(count_subagent_files)"
      [[ "$SUBAGENT_FILES" -gt 0 ]] && break
    done
  fi
  if [[ "$PARENT_LINKED_COUNT" -eq 0 || "$SUBAGENT_FILES" -eq 0 ]]; then
    write_error_manifest "no_subagents_spawned" \
      "$(jq -nc \
          --argjson parent_linked_count "$PARENT_LINKED_COUNT" \
          --argjson subagent_transcript_count "$SUBAGENT_FILES" \
          '{parent_linked_count: $parent_linked_count, subagent_transcript_count: $subagent_transcript_count}')"
    echo "ERROR: scenario requires subagents but none spawned (parent_linked=$PARENT_LINKED_COUNT, files=$SUBAGENT_FILES)" >&2
    exit 1
  fi
fi

# --- Curate the staged fixture ------------------------------------------
# The committed-to-replaydata location of the curated artifacts is:
#   <staging>/replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl
"$REPO_ROOT/tools/curate-lifecycle-fixture.sh" \
  -d "$STAGING/replaydata/agents" \
  "$RECORDING" "$ACTUAL_UUID" "$TRANSCRIPT" "$ADAPTER" "$SCENARIO"

# Aider's curated fixture is markdown (curate-lifecycle-fixture.sh keeps
# the native extension); other adapters are JSONL.
if [[ "$ADAPTER" == "aider" ]]; then
  TRANSCRIPT_EXT="md"
else
  TRANSCRIPT_EXT="jsonl"
fi
STAGED_TRANSCRIPT="$STAGING/replaydata/agents/$ADAPTER/scenarios/$SCENARIO/transcript.$TRANSCRIPT_EXT"

# --- Build replay reports -----------------------------------------------
# precheck.sh pre-built the replay binary under .build/refresh/bin/replay
# so we avoid `go run` recompile on each cell invocation.
# The replay CLI exits non-zero when extended-check finds daemon-vs-simulator
# transition mismatches. The report is still written and is the authoritative
# artifact — extended-check is informational. Treat nonzero as "report OK,
# warnings present"; only a missing report file counts as a real failure.
replay_one() {
  local transcript="$1" out="$2"
  (cd "$REPO_ROOT" && "$REPLAY_BIN" --quiet --out "$out" "$transcript") || true
  [[ -s "$out" ]] || { echo "replay failed (no report written) for $transcript" >&2; return 1; }
}

replay_one "$STAGED_TRANSCRIPT" "$STAGING/reports/staged.json" || exit 1

COMMITTED_TRANSCRIPT="$REPO_ROOT/replaydata/agents/$ADAPTER/scenarios/$SCENARIO/transcript.$TRANSCRIPT_EXT"
if [[ -f "$COMMITTED_TRANSCRIPT" ]]; then
  replay_one "$COMMITTED_TRANSCRIPT" "$STAGING/reports/committed.json" || exit 1
  COMMITTED_PRESENT=true
else
  COMMITTED_PRESENT=false
fi

# --- Manifest -----------------------------------------------------------
jq -n \
  --arg adapter "$ADAPTER" \
  --arg scenario "$SCENARIO" \
  --arg session_uuid "$ACTUAL_UUID" \
  --arg staging "$STAGING" \
  --arg raw_recording "$RECORDING" \
  --arg source_transcript "$TRANSCRIPT" \
  --arg staged_fixture_transcript "$STAGED_TRANSCRIPT" \
  --arg staged_fixture_events "$STAGING/replaydata/agents/$ADAPTER/scenarios/$SCENARIO/events.jsonl" \
  --arg staged_report "$STAGING/reports/staged.json" \
  --argjson committed_fixture_present "$COMMITTED_PRESENT" \
  --arg committed_report "$STAGING/reports/committed.json" \
  --arg driver_exit_reason "$DRIVER_REASON" \
  --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
  --argjson timeout_seconds "$TIMEOUT_S" \
  '{adapter: $adapter,
    scenario: $scenario,
    session_uuid: $session_uuid,
    verdict: "STAGED",
    staging: $staging,
    raw_recording: $raw_recording,
    source_transcript: $source_transcript,
    staged_fixture_transcript: $staged_fixture_transcript,
    staged_fixture_events: $staged_fixture_events,
    staged_report: $staged_report,
    committed_fixture_present: $committed_fixture_present,
    committed_report: $committed_report,
    driver_exit_reason: $driver_exit_reason,
    daemon_shutdown: $daemon_shutdown,
    timeout_seconds: $timeout_seconds}' \
  > "$MANIFEST"

echo "staged: $STAGED_TRANSCRIPT"
echo "manifest: $MANIFEST"
