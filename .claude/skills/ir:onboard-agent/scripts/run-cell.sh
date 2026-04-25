#!/usr/bin/env bash
# run-cell.sh — execute one (adapter, scenario) cell end-to-end.
#
# Pipeline:
#   precheck.sh  →  spawn isolated irrlichd --record
#                →  drive-<adapter>.sh (runs the agent under timeout)
#                →  SIGINT → 6s grace → SIGTERM → SIGKILL the daemon
#                →  resolve transcript path from session UUID
#                →  scripts/curate-lifecycle-fixture.sh -d <staging>/testdata
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
#   testdata/replay/<adapter>/<scenario>.{jsonl,events.jsonl}  — staged fixture
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
CELL_JSON="$(jq --arg s "$SCENARIO" --arg a "$ADAPTER" '
  .scenarios[]
  | select(.name == $s)
  | select(.by_adapter[$a])
  | {
      description,
      requires,
      verify,
      prompt: .by_adapter[$a].prompt,
      settings: .by_adapter[$a].settings,
      timeout_seconds: .by_adapter[$a].timeout_seconds
    }
' "$SCENARIOS_JSON")"
if [[ -z "$CELL_JSON" || "$CELL_JSON" == "null" ]]; then
  echo "cell not found: scenario=$SCENARIO adapter=$ADAPTER (either unknown or missing-prompt)" >&2
  exit 1
fi

TIMEOUT_S="$(jq -r '.timeout_seconds' <<<"$CELL_JSON")"
PROMPT="$(jq -r '.prompt' <<<"$CELL_JSON")"

# --- Precheck ------------------------------------------------------------
"$SCRIPT_DIR/precheck.sh" "$ADAPTER" "$SCENARIOS_JSON"

# --- Staging -------------------------------------------------------------
TS="$(date -u +%Y%m%dT%H%M%S)"
STAGING="$REPO_ROOT/.build/refresh/$ADAPTER/$SCENARIO-$TS"
# Hard safety: staging must live under .build/refresh/. Guards against
# ADAPTER/SCENARIO arguments containing path traversal ("..") that
# survived the jq lookup; cheap string test.
if [[ "$STAGING" != "$REPO_ROOT/.build/refresh/"* ]] || [[ "$STAGING" == *"/testdata/"* ]]; then
  echo "refusing to stage outside .build/refresh/: $STAGING" >&2
  exit 1
fi
mkdir -p "$STAGING/recordings" "$STAGING/testdata/replay/$ADAPTER" "$STAGING/reports"

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
DRIVER="$SCRIPT_DIR/drive-$ADAPTER.sh"
[[ -x "$DRIVER" ]] || { echo "driver missing: $DRIVER" >&2; exit 1; }
set +e
"$DRIVER" "$STAGING" "$UUID" "$TIMEOUT_S" "$STAGING/settings.json" "$PROMPT"
set -e
DRIVER_REASON="$(cat "$STAGING/driver.exit-reason" 2>/dev/null || echo "unknown")"

# Drain the daemon now — the recorder must flush before we curate.
cleanup
trap - EXIT

# --- Resolve the transcript path ----------------------------------------
# Claude Code writes transcripts to ~/.claude/projects/<slug>/<UUID>.jsonl.
# Stat the expected path under each slug dir (O(#projects)) rather than
# walking the whole tree with `find`. Poll up to 30s.
TRANSCRIPT=""
for _ in $(seq 1 60); do
  for slug_dir in "$HOME"/.claude/projects/*/; do
    candidate="$slug_dir$UUID.jsonl"
    if [[ -f "$candidate" ]]; then
      TRANSCRIPT="$candidate"
      break 2
    fi
  done
  sleep 0.5
done

# --- Locate the recording file ------------------------------------------
RECORDING="$(find "$STAGING/recordings" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | head -n1)"

MANIFEST="$STAGING/run-manifest.json"
DAEMON_SHUTDOWN="$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo "unknown")"

if [[ -z "$TRANSCRIPT" || -z "$RECORDING" ]]; then
  jq -n \
    --arg adapter "$ADAPTER" \
    --arg scenario "$SCENARIO" \
    --arg session_uuid "$UUID" \
    --argjson transcript_found "$([[ -n "$TRANSCRIPT" ]] && echo true || echo false)" \
    --argjson recording_found "$([[ -n "$RECORDING" ]] && echo true || echo false)" \
    --arg driver_exit_reason "$DRIVER_REASON" \
    --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
    --arg staging "$STAGING" \
    '{adapter: $adapter,
      scenario: $scenario,
      session_uuid: $session_uuid,
      verdict: "ERROR",
      error: "transcript_or_recording_missing",
      transcript_found: $transcript_found,
      recording_found: $recording_found,
      driver_exit_reason: $driver_exit_reason,
      daemon_shutdown: $daemon_shutdown,
      staging: $staging}' \
    > "$MANIFEST"
  echo "ERROR: transcript=${TRANSCRIPT:-missing} recording=${RECORDING:-missing}" >&2
  exit 1
fi

# --- Subagent probe -----------------------------------------------------
# If the scenario requires the `subagents` capability, the run is only
# meaningful if the parent actually emitted Agent tool calls and the daemon
# saw the resulting parent_linked events. Fail cleanly here so the manifest
# carries a structured reason instead of producing an empty .subagents/ dir
# downstream.
REQUIRES_SUBAGENTS="$(jq -r '.requires | index("subagents") // empty' <<<"$CELL_JSON")"
if [[ -n "$REQUIRES_SUBAGENTS" ]]; then
  PARENT_LINKED_COUNT="$(jq -c --arg sid "$UUID" \
    'select(.kind=="parent_linked" and .parent_session_id==$sid)' \
    "$RECORDING" | wc -l | tr -d ' ')"
  SUBAGENT_DIR="$(dirname "$TRANSCRIPT")/$UUID/subagents"
  count_subagent_files() {
    if [[ -d "$SUBAGENT_DIR" ]]; then
      find "$SUBAGENT_DIR" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | wc -l | tr -d ' '
    else
      echo 0
    fi
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
    jq -n \
      --arg adapter "$ADAPTER" \
      --arg scenario "$SCENARIO" \
      --arg session_uuid "$UUID" \
      --argjson parent_linked_count "$PARENT_LINKED_COUNT" \
      --argjson subagent_transcript_count "$SUBAGENT_FILES" \
      --arg driver_exit_reason "$DRIVER_REASON" \
      --arg daemon_shutdown "$DAEMON_SHUTDOWN" \
      --arg staging "$STAGING" \
      '{adapter: $adapter,
        scenario: $scenario,
        session_uuid: $session_uuid,
        verdict: "ERROR",
        error: "no_subagents_spawned",
        parent_linked_count: $parent_linked_count,
        subagent_transcript_count: $subagent_transcript_count,
        driver_exit_reason: $driver_exit_reason,
        daemon_shutdown: $daemon_shutdown,
        staging: $staging}' \
      > "$MANIFEST"
    echo "ERROR: scenario requires subagents but none spawned (parent_linked=$PARENT_LINKED_COUNT, files=$SUBAGENT_FILES)" >&2
    exit 1
  fi
fi

# --- Curate the staged fixture ------------------------------------------
# The committed-to-testdata location of the curated artifacts is:
#   <staging>/testdata/replay/<adapter>/<scenario>.{jsonl,events.jsonl}
"$REPO_ROOT/scripts/curate-lifecycle-fixture.sh" \
  -d "$STAGING/testdata/replay" \
  "$RECORDING" "$UUID" "$TRANSCRIPT" "$ADAPTER" "$SCENARIO"

STAGED_TRANSCRIPT="$STAGING/testdata/replay/$ADAPTER/$SCENARIO.jsonl"

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

COMMITTED_TRANSCRIPT="$REPO_ROOT/testdata/replay/$ADAPTER/$SCENARIO.jsonl"
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
  --arg session_uuid "$UUID" \
  --arg staging "$STAGING" \
  --arg raw_recording "$RECORDING" \
  --arg source_transcript "$TRANSCRIPT" \
  --arg staged_fixture_transcript "$STAGED_TRANSCRIPT" \
  --arg staged_fixture_events "$STAGING/testdata/replay/$ADAPTER/$SCENARIO.events.jsonl" \
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
