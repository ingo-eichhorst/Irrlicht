#!/usr/bin/env bash
# run-cell.sh — execute one (adapter, scenario) cell end-to-end.
#
# Pipeline:
#   precheck.sh  →  spawn isolated irrlichd --record
#                →  drive-<adapter>.sh (runs the agent with timeout + budget)
#                →  SIGINT → 6s grace → SIGTERM → SIGKILL the daemon
#                →  resolve transcript path from session UUID
#                →  scripts/curate-lifecycle-fixture.sh -d <staging>/testdata
#                →  go run ./core/cmd/replay against staged + committed fixtures
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
# Hard safety: staging must not resolve under testdata/ by any trick.
case "$(cd "$(dirname "$STAGING")" 2>/dev/null && pwd)/$(basename "$STAGING")" in
  "$REPO_ROOT"/testdata/*)
    echo "refusing to stage under testdata/: $STAGING" >&2
    exit 1
    ;;
esac
mkdir -p "$STAGING/recordings" "$STAGING/testdata/replay/$ADAPTER" "$STAGING/reports"

# Scenario's settings blob → staging file, passed to driver as a path.
# This avoids --settings <json-blob> shell-quoting fragility.
jq '.settings' <<<"$CELL_JSON" > "$STAGING/settings.json"

UUID="$(uuidgen | tr '[:upper:]' '[:lower:]')"

# --- Launch isolated daemon ---------------------------------------------
DAEMON="$REPO_ROOT/.build/refresh/bin/irrlichd"
[[ -x "$DAEMON" ]] || { echo "daemon binary missing: $DAEMON (precheck should have built it)" >&2; exit 1; }

DAEMON_LOG="$STAGING/daemon.log"
IRRLICHT_RECORDINGS_DIR="$STAGING/recordings" \
  IRRLICHT_BIND_ADDR="127.0.0.1:7837" \
  "$DAEMON" --record >"$DAEMON_LOG" 2>&1 &
DAEMON_PID=$!
echo "daemon started (pid $DAEMON_PID)"

# Cleanup: graceful shutdown on any exit path.
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

# Wait up to 10s for the unix socket to appear — signal the daemon has
# accepted at least one listener.
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
DRIVER_EXIT=$?
set -e
DRIVER_REASON="$(cat "$STAGING/driver.exit-reason" 2>/dev/null || echo "unknown")"

# --- Drain + stop daemon (triggered by cleanup via EXIT trap) ----------
# Do an explicit drain now so the next steps see a complete recording.
cleanup
trap - EXIT

# --- Resolve the transcript path ----------------------------------------
# Claude Code writes transcripts to ~/.claude/projects/<slug>/<UUID>.jsonl.
# Poll up to 30s for the file to appear.
TRANSCRIPT=""
for _ in $(seq 1 60); do
  TRANSCRIPT="$(find "$HOME/.claude/projects" -type f -name "$UUID.jsonl" 2>/dev/null | head -n1)"
  [[ -n "$TRANSCRIPT" ]] && break
  sleep 0.5
done

# --- Locate the recording file ------------------------------------------
RECORDING="$(find "$STAGING/recordings" -maxdepth 1 -name '*.jsonl' -type f 2>/dev/null | head -n1)"

MANIFEST="$STAGING/run-manifest.json"

if [[ -z "$TRANSCRIPT" || -z "$RECORDING" ]]; then
  cat > "$MANIFEST" <<EOF
{
  "adapter": "$ADAPTER",
  "scenario": "$SCENARIO",
  "session_uuid": "$UUID",
  "verdict": "ERROR",
  "error": "transcript_or_recording_missing",
  "transcript_found": $([ -n "$TRANSCRIPT" ] && echo true || echo false),
  "recording_found": $([ -n "$RECORDING" ] && echo true || echo false),
  "driver_exit_reason": "$DRIVER_REASON",
  "daemon_shutdown": "$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo unknown)",
  "staging": "$STAGING"
}
EOF
  echo "ERROR: transcript=${TRANSCRIPT:-missing} recording=${RECORDING:-missing}" >&2
  exit 1
fi

# --- Curate the staged fixture ------------------------------------------
# The committed-to-testdata location of the curated artifacts is:
#   <staging>/testdata/replay/<adapter>/<scenario>.{jsonl,events.jsonl}
"$REPO_ROOT/scripts/curate-lifecycle-fixture.sh" \
  -d "$STAGING/testdata/replay" \
  "$RECORDING" "$UUID" "$TRANSCRIPT" "$ADAPTER" "$SCENARIO"

STAGED_TRANSCRIPT="$STAGING/testdata/replay/$ADAPTER/$SCENARIO.jsonl"

# --- Build replay reports -----------------------------------------------
# The replay CLI exits non-zero when extended-check finds daemon-vs-simulator
# transition mismatches. The report is still written and is the authoritative
# artifact — extended-check is informational. Treat nonzero as "report OK,
# warnings present"; only a missing report file counts as a real failure.
replay_one() {
  local transcript="$1" out="$2"
  (cd "$REPO_ROOT" && go run ./core/cmd/replay --quiet --out "$out" "$transcript") || true
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
cat > "$MANIFEST" <<EOF
{
  "adapter": "$ADAPTER",
  "scenario": "$SCENARIO",
  "session_uuid": "$UUID",
  "verdict": "STAGED",
  "staging": "$STAGING",
  "raw_recording": "$RECORDING",
  "source_transcript": "$TRANSCRIPT",
  "staged_fixture_transcript": "$STAGED_TRANSCRIPT",
  "staged_fixture_events": "$STAGING/testdata/replay/$ADAPTER/$SCENARIO.events.jsonl",
  "staged_report": "$STAGING/reports/staged.json",
  "committed_fixture_present": $COMMITTED_PRESENT,
  "committed_report": "$STAGING/reports/committed.json",
  "driver_exit_reason": "$DRIVER_REASON",
  "daemon_shutdown": "$(cat "$STAGING/daemon.shutdown" 2>/dev/null || echo unknown)",
  "timeout_seconds": $TIMEOUT_S
}
EOF

echo "staged: $STAGED_TRANSCRIPT"
echo "manifest: $MANIFEST"
