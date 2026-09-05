#!/usr/bin/env bash
# classify-failure.sh — categorize a failed run-cell.sh staging dir.
#
# Usage:
#   scripts/lib/classify-failure.sh <staging-dir>
#
# Outputs JSON to stdout: {"code": "<code>", "summary": "...", "evidence": "..."}
#
# It also reads a run-cell-multi.sh staging dir: that rig writes the same
# run-manifest.json envelope (its own write_error_manifest "mirrors run-cell.sh's
# contract"), so its error codes are classified here too. Its per-adapter driver
# logs live one directory down, so only the manifest-driven arms can fire for it
# — which is exactly why every code it writes needs an arm rather than falling
# to the log heuristics below.
#
# Codes:
#   cli_not_found, cli_too_old, auth_failed, daemon_dirty, daemon_not_ready,
#   working_tree_dirty, transcript_missing, timeout, daemon_crashed,
#   driver_session_leaked, driver_teardown_unverifiable, driver_pid_unrecorded,
#   replay_failed, unknown
#
# THE MANIFEST ARMS ARE KEPT IN SYNC MECHANICALLY (#1825). Every arm label in
# the `case "$err_code"` block below is a string some OTHER script types into a
# manifest, and nothing but a matching literal connects the two. That seam had
# silently parted twice before it was noticed — see the case block's own comment
# — so lib/classify-failure_test.sh now walks both rigs' write_error_manifest
# call sites and fails when a code has no arm here, or an arm here names a code
# no rig writes. Add an arm in the same commit as a new error code.

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

# Manifest-driven classifications first (the rig wrote them deliberately).
#
# WHAT WENT WRONG HERE BEFORE, because these arms are the reason the tripwire in
# the test exists. Both of the original three arms were DEAD, and both were born
# that way in 6d24cb14 (2026-04-25), the commit that added this file:
#
#   transcript_or_recording_missing — run-cell.sh had been renamed to write
#     `transcript_recording_or_uuid_missing` seven hours earlier (a511ad11), so
#     this arm never matched anything, from its first day to 2026-08-25. Every
#     "the daemon didn't see the agent's session" run for those 4 months was
#     reported to the record skill as `unknown`, i.e. as "inspect the staging
#     dir by hand" — and `unknown` is also what a genuinely unrecognized failure
#     prints, so nothing distinguished "not classified" from "not classifiable".
#
#   wall_clock_timeout — never written by ANY script in this repo's history
#     (`git log -S wall_clock_timeout --all` returns only 6d24cb14, this file).
#     It made `timeout` an unreachable output while record/SKILL.md's retry
#     policy keys on exactly that code. Replaced below by reading the field the
#     rig really does write: driver.exit-reason, which contracts.sh's drive_exit
#     sets to `timeout` and write_error_manifest copies into the manifest.
#
# Neither was a typo anyone could have seen by reading one file, which is the
# whole point: the writer and the matcher are in different scripts, and a
# never-matching `case` arm is indistinguishable from a never-taken one.
if [[ -f "$MANIFEST" ]]; then
  err_code=$(jq -r '.error // empty' "$MANIFEST" 2>/dev/null || echo "")
  case "$err_code" in
    transcript_recording_or_uuid_missing) emit "transcript_missing" "Daemon didn't see the agent's session" "$err_code" ;;
    no_recording)                         emit "transcript_missing" "Daemon produced no recording for this run" "$err_code" ;;
    no_subagents_spawned)                 emit "transcript_missing" "Scenario requires subagents but none spawned" "$err_code" ;;
    daemon_socket_missing)                emit "daemon_not_ready"   "Recording daemon never opened its socket" "$err_code" ;;
    replay_failed)                        emit "replay_failed"      "Replay produced no report for a staged fixture" "$(jq -r '.failed_adapter // empty' "$MANIFEST" 2>/dev/null || echo "")" ;;
    # #1828: driver.pid never being written is a THIRD thing, and deliberately
    # NOT folded into either teardown verdict below. It fires before
    # check_tmux_teardown is ever asked anything (run-cell.sh's
    # DRIVER_PID_PROBLEM comment has the detail): the pid-wrapper died before
    # it could exec the driver, almost certainly because $STAGING was not
    # writable, so there is no run identity and nothing of this run's for tmux
    # to be holding. Folding it into driver_teardown_unverifiable would send
    # the operator to look at tmux for a problem tmux was never asked about —
    # the very conflation #1825 fixed one layer up. The other two driver.pid
    # failures (empty, non-numeric) DO mean the driver started, so they stay
    # driver_teardown_unverifiable: a live agent may genuinely be out there
    # under a name this run can no longer match, which is exactly what that
    # code already says.
    driver_pid_unrecorded) emit "driver_pid_unrecorded" \
      "driver.pid was never written — check that $STAGING is writable and that the driver process ever started; tmux was not asked anything" \
      "$(jq -r '.tmux_teardown_detail // empty' "$MANIFEST" 2>/dev/null || echo "")" ;;
    # #1825 / AC4. Two codes, two classifications, deliberately NOT collapsed:
    # a leaked session means a live agent process is still on this host and the
    # operator has a `tmux kill-session` to run before retrying, while an
    # unreadable check means nothing was established either way. Printing one
    # answer for both is the bug #1825 is about, one layer up.
    driver_tmux_session_survived)   emit "driver_session_leaked" \
      "Driver returned but a tmux session carrying its pid is still alive — kill it before retrying" \
      "$(jq -r '.tmux_teardown_detail // empty' "$MANIFEST" 2>/dev/null || echo "")" ;;
    driver_tmux_teardown_unreadable) emit "driver_teardown_unverifiable" \
      "The tmux-teardown check could not be made, so whether the driver leaked is UNKNOWN — not a pass" \
      "$(jq -r '.tmux_teardown_detail // empty' "$MANIFEST" 2>/dev/null || echo "")" ;;
    # Deliberately no emit: run-cell-multi.sh writes driver_failed when a driver
    # exited non-zero or resolved no session, which is the least informative
    # thing known about the failure. The driver-log and daemon-log heuristics
    # below refine it into cli_not_found / auth_failed / daemon_crashed, so
    # falling through reaches a better answer than the manifest carries. The arm
    # exists so the sync tripwire sees a decision rather than an omission.
    driver_failed) ;;
    *) ;;
  esac

  # Timeout, from the field the rig actually writes. run-cell.sh puts the
  # driver's own exit reason in .driver_exit_reason; run-cell-multi.sh puts one
  # per adapter in the .driver_exit_reasons object. This runs AFTER the code
  # arms on purpose — it can only turn what would have been `unknown` into
  # `timeout`, never reclassify a code that was matched above.
  timed_out=$(jq -r '
      [ (.driver_exit_reason // empty) ] + [ (.driver_exit_reasons // {}) | .[] ]
      | map(select(. == "timeout")) | length' \
    "$MANIFEST" 2>/dev/null || echo 0)
  if [[ "${timed_out:-0}" != "0" ]]; then
    emit "timeout" "Driver hit its wall-clock timeout" "driver.exit-reason=timeout"
  fi
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

# Daemon-side crashes (#1018: DAEMON_LOG was already staged but never read —
# a Go panic or fatal runtime error here is the richest signal available for
# daemon-caused failures, ahead of falling to unknown).
if [[ -f "$DAEMON_LOG" ]] && grep -qE "^panic:|fatal error:" "$DAEMON_LOG" 2>/dev/null; then
  emit "daemon_crashed" "irrlichd panicked or hit a fatal runtime error" "$(grep -m1 -E "^panic:|fatal error:" "$DAEMON_LOG")"
fi

emit "unknown" "Run failed for an unrecognized reason; inspect $STAGING manually"
