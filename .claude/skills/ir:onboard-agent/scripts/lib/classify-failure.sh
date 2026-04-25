#!/usr/bin/env bash
# classify-failure.sh — categorize a failed run-cell.sh staging dir.
#
# Usage:
#   scripts/lib/classify-failure.sh <staging-dir>
#
# Outputs JSON to stdout: {"code": "<code>", "summary": "...", "evidence": "..."}
#
# Codes:
#   cli_not_found, cli_too_old, auth_failed, daemon_dirty,
#   working_tree_dirty, transcript_missing, timeout, unknown

set -euo pipefail

STAGING="${1:-}"
[[ -n "$STAGING" && -d "$STAGING" ]] || { echo '{"code":"unknown","summary":"missing staging dir"}' ; exit 0; }

DRIVER_LOG="$STAGING/driver.log"
DAEMON_LOG="$STAGING/daemon.log"
MANIFEST="$STAGING/run-manifest.json"
PRECHECK_LOG="$STAGING/precheck.log"

emit() {
  local code="$1" summary="$2" evidence="${3:-}"
  jq -nc \
    --arg code "$code" \
    --arg summary "$summary" \
    --arg evidence "$evidence" \
    '{code: $code, summary: $summary, evidence: $evidence}'
  exit 0
}

# Manifest-driven classifications first (run-cell.sh wrote them deliberately).
if [[ -f "$MANIFEST" ]]; then
  err_code=$(jq -r '.error // empty' "$MANIFEST" 2>/dev/null || echo "")
  case "$err_code" in
    transcript_or_recording_missing) emit "transcript_missing" "Daemon didn't see the agent's session" "$err_code" ;;
    wall_clock_timeout)              emit "timeout"            "Scenario timed out"                    "$err_code" ;;
    no_subagents_spawned)            emit "transcript_missing" "Scenario requires subagents but none spawned" "$err_code" ;;
  esac
fi

# Precheck refusals.
if [[ -f "$PRECHECK_LOG" ]]; then
  if grep -q "another irrlichd is running" "$PRECHECK_LOG" 2>/dev/null; then
    emit "daemon_dirty" "Another irrlichd is running on port 7837" "$(grep -m1 "irrlichd" "$PRECHECK_LOG")"
  fi
  if grep -q "uncommitted changes" "$PRECHECK_LOG" 2>/dev/null; then
    emit "working_tree_dirty" "replaydata/agents/ has uncommitted changes" "$(grep -m1 "uncommitted" "$PRECHECK_LOG")"
  fi
  if grep -qE "command -v|not found" "$PRECHECK_LOG" 2>/dev/null; then
    emit "cli_not_found" "Adapter CLI is not on PATH" "$(grep -m1 -E "command -v|not found" "$PRECHECK_LOG")"
  fi
  if grep -qE "below pinned minimum|version" "$PRECHECK_LOG" 2>/dev/null && grep -q "fail" "$PRECHECK_LOG" 2>/dev/null; then
    emit "cli_too_old" "Adapter CLI version below min_versions" "$(grep -m1 -E "minimum|version" "$PRECHECK_LOG")"
  fi
fi

# Driver-side auth failures.
if [[ -f "$DRIVER_LOG" ]]; then
  if grep -qE "command not found|No such file" "$DRIVER_LOG" 2>/dev/null; then
    emit "cli_not_found" "Adapter CLI not found at runtime" "$(grep -m1 -E "command not found|No such file" "$DRIVER_LOG")"
  fi
  if grep -qiE "please log in|authentication required|401|unauthorized|api key not set|no api key" "$DRIVER_LOG" 2>/dev/null; then
    emit "auth_failed" "Adapter is installed but not authenticated" "$(grep -m1 -iE "log in|auth|401|api key" "$DRIVER_LOG")"
  fi
fi

emit "unknown" "Run failed for an unrecognized reason; inspect $STAGING manually"
