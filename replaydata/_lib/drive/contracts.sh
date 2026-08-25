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

# ONE FILE OF THE STAGING CONTRACT IS NOT WRITTEN BY THE DRIVER (#1825).
#
#   <staging>/driver.pid — written by run-cell.sh, before the driver starts, and
#   never read or written by any driver. Everything else under <staging> that
#   the contract names is produced by emit_session_contract below; this one is
#   produced by the caller, about the driver, and that asymmetry is exactly what
#   makes it easy to break without noticing.
#
# WHAT DEPENDS ON IT. run-cell.sh's tmux-teardown gate uses that pid as the
# RUN'S IDENTITY: after the driver returns, it asks tmux for its session list
# and fails the cell if any session carries the pid as a '-'-delimited field —
# which works only because every interactive driver embeds its own `$$` in every
# session name it creates (`claudecode-onboard-<ts>-<pid>`, `ocdrv-<pid>-<ts>`,
# `aider-onboard-<uuid8>-<pid>`, …). Two ways to break the gate silently:
#
#   1. Change how the driver is INVOKED. run-cell.sh gets the pid by having
#      `bash -c` write its own `$$` and then `exec` the driver into that same
#      process, so driver.pid holds the pid the driver will actually run as. Any
#      wrapper that forks between the write and the driver — a pipeline, a
#      `( … )`, `nohup`, a `timeout` that does not exec — makes driver.pid name
#      a DIFFERENT process from the one whose pid is in the session names, and
#      the gate then passes on every run without ever matching anything.
#      run-cell-multi.sh does not need the file (it backgrounds its drivers, so
#      `$!` is that same pid) and therefore does not write one.
#   2. Change how the driver NAMES its tmux sessions. Drop `$$` from the name,
#      or bury it inside a larger field (`pid<pid>`, `<ts><pid>`), and the same
#      thing happens — the gate looks, matches nothing, and reports "clean".
#      A static per-driver tripwire under tools/onboarding-factory/internal/
#      asserts the `$$`-as-a-whole-field convention for every adapter, so this
#      half is caught; the invocation half above is only caught by the wiring
#      assertions in scripts/lib/tmux-teardown-check_test.sh.
#
# Both failure modes are silent PASSES, not errors, which is why they are
# written down here rather than left to be rediscovered.
#
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
  return 0
}

# drive_exit maps EXIT_REASON to the process exit code and exits. Every case
# (including the catch-all) calls exit, so this function never returns
# normally — no trailing `return`, which would be unreachable dead code and is
# what SC2317 flags.
#
# Note how that code is named: NOT by opening a comment line with the linter's
# own name. A comment whose FIRST word after `#` is that name is parsed as a
# DIRECTIVE, and an unparseable one (SC1073/SC1072) makes the analysis of the
# WHOLE file be abandoned — every later finding silently disappears. This file
# carried exactly that from #508 until #1687, during which shellcheck's only
# output for it was the two parse errors. tools/bash-lint.sh keeps SC1072/SC1073
# inside its severity floor so the construct fails the gate rather than reading
# as a clean file.
drive_exit() {
  case "$EXIT_REASON" in
    ok)            exit 0 ;;
    timeout)       exit 124 ;;
    nonzero\(*\))  exit "${EXIT_REASON//[!0-9]/}" ;;
    *)             exit 1 ;;
  esac
}
