#!/usr/bin/env bash
# contracts.sh — shared staging-contract emission for the interactive recording
# drivers (#508 #3). Extracted from drive-codex/pi-interactive.sh, whose
# epilogues were identical except the PRIMARY session.uuid source (codex uses
# daemon_sid of the rollout path; pi uses the bare first-line UUID). Requires
# slots.sh (for daemon_sid) sourced first.
#
# Driver-owned globals: STAGING DRIVER_LOG EXIT_REASON N_SLOTS, the
# SES_TRANSCRIPT array.
#
# Sourced as a library; MUST NOT call `set` at top level.

# emit_session_contract <primary_session_uuid>
#   Finalizes the combined stdout log and writes the staging contract:
#   driver.exit-reason, session.uuid (=$1) + transcript.path (slot 1), and the
#   multi-session session.uuids / transcript.paths lists (the daemon_sid of
#   each slot's transcript, plus the absolute transcript path). A single-slot
#   run leaves the lists with one entry — run-cell.sh's multi-session branch is
#   a no-op when there's only one line.
emit_session_contract() {
  local primary_uuid="$1" i
  # Keep a combined .stdout for backward-compat with any tooling that reads it.
  cat "$DRIVER_LOG".stdout.* > "$DRIVER_LOG.stdout" 2>/dev/null || true
  echo "$EXIT_REASON" > "$STAGING/driver.exit-reason"
  echo "$primary_uuid" > "$STAGING/session.uuid"
  echo "${SES_TRANSCRIPT[1]}" > "$STAGING/transcript.path"
  : > "$STAGING/session.uuids"
  : > "$STAGING/transcript.paths"
  for (( i = 1; i <= N_SLOTS; i++ )); do
    echo "$(daemon_sid "${SES_TRANSCRIPT[$i]}")" >> "$STAGING/session.uuids"
    echo "${SES_TRANSCRIPT[$i]}" >> "$STAGING/transcript.paths"
  done
}

# drive_exit maps EXIT_REASON to the process exit code and exits.
drive_exit() {
  case "$EXIT_REASON" in
    ok)            exit 0 ;;
    timeout)       exit 124 ;;
    nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
    *)             exit 1 ;;
  esac
}
