#!/usr/bin/env bash
# swift-suite.sh — run the macOS Swift test suite so that a hang or an abort is
# a bounded, legible failure instead of a silently truncated run (#1523).
#
# This file is sourced, not executed:
#
#   . "$SCRIPT_DIR/lib/swift-suite.sh"
#   swift_suite_run "$log" swift test --skip LauncherHarnessTests
#   swift_suite_verdict "$?" "$log"
#
# Why any of this exists. XCTest's stall detector responds to a hung
# expectation by calling `abort()` on the whole process. The run then stops
# partway — 33 of 40 suites, in the measured case — and the aggregate total
# never prints. Every suite that DID report says "0 failures", because none of
# them failed; the ones that would have failed simply never ran. So the output
# reads as healthy, and the only signal is an exit code that is easy to
# attribute to the cosmetic-looking `error: Exited with unexpected signal code
# 6` line trailing the log. That misreading has already happened twice in this
# repo: once when a truncated run was reported as "every other Swift suite
# passes", and once in #1044 when a real appearance bug was written off as
# toolchain antialiasing.
#
# The three checks below are therefore about the *shape* of the run, not about
# whether any assertion failed:
#
#   completed  — the test bundle reported its own result, so nothing was
#                dropped after the last line we can see.
#   ran tests  — it executed at least one test, so "completed" is not vacuous.
#   exit code  — the ordinary signal, kept but no longer trusted alone.
#
# `swift_suite_completed` keys on the BUNDLE line rather than on the presence
# of an `Executed N tests` aggregate, and the difference is the whole point:
# measured against real logs, a truncated run still contains 29-33 per-suite
# `Executed N tests` lines, so a grep for that string reports a truncated run
# as complete. Only the bundle's own summary is written once, at the end.

# Seconds before a run is declared hung. Deliberately far above the observed
# runtime (~2s idle, ~30s under heavy load) — this is a containment for a
# process that will otherwise sit forever, not a performance budget. It
# mirrors macos-swift.yml's `timeout-minutes`, which exists for the same
# reason: a job stuck `in_progress` reads as "still queued".
SWIFT_SUITE_TIMEOUT="${SWIFT_SUITE_TIMEOUT:-600}"

# Every grep here passes -a. That is not defensive noise: `swift test` emits
# ANSI colour and erase sequences, and a grep that classifies the stream as
# binary on one control byte matches NOTHING and reports success. The default
# `grep` on a developer machine here is ugrep, which does exactly that, and it
# fooled an abort-check run against this very issue's output.
SWIFT_SUITE_GREP_OPTS="-a"

# swift_suite_completed <log> — did the test bundle report its own result?
swift_suite_completed() {
  grep $SWIFT_SUITE_GREP_OPTS -qE "Test Suite '.*\.xctest' (passed|failed) at" "$1"
}

# swift_suite_ran_tests <log> — did it execute at least one test? The vacuity
# guard: a bundle that reports "Executed 0 tests" is a gate that checked
# nothing, which this repo treats as a failure rather than a pass.
swift_suite_ran_tests() {
  grep $SWIFT_SUITE_GREP_OPTS -qE 'Executed [1-9][0-9]* test' "$1"
}

# swift_suite_last_test <log> — the last test to *start*, which for a hang or
# an abort is the one that caused it. Only usable because swift_suite_run
# allocates a pty: XCTest block-buffers when stdout is not a TTY, so a hung run
# otherwise produces an empty log and cannot be attributed at all.
swift_suite_last_test() {
  local last
  last=$(grep $SWIFT_SUITE_GREP_OPTS "' started" "$1" 2>/dev/null | tail -1)
  [[ -n "$last" ]] && echo "  last test to start: ${last#Test Case }"
  return 0
}

# swift_suite_verdict <exit-code> <log> — 0 only if the run both passed and
# demonstrably finished. Prints a diagnosis naming which of the two failed.
swift_suite_verdict() {
  local rc="${1:-}" log="${2:-}" ok=0

  if [[ -z "$rc" || -z "$log" ]]; then
    echo "swift_suite_verdict: needs <exit-code> <log>" >&2
    return 1
  fi
  if [[ ! -f "$log" ]]; then
    echo "swift test produced no log at '$log' — treating the run as failed." >&2
    return 1
  fi
  # An EMPTY log is a different diagnosis from a truncated one and deserves its
  # own line: the suite never started, rather than starting and stopping partway.
  # Observed while building this file — `script` cannot allocate a pty when the
  # system has run out of them (which a burst of kill -9'd runs will do), and it
  # then exits non-zero having produced nothing. Reporting that as "truncated"
  # sends the reader looking for a test that never ran.
  if [[ ! -s "$log" ]]; then
    echo "swift test produced NO output at all — the run never started." >&2
    echo "  A pty that could not be allocated looks exactly like this; so does a" >&2
    echo "  toolchain that failed to launch. Re-run, and check \`script -q /dev/null true\`." >&2
    return 1
  fi

  if [[ "$rc" -eq 124 ]]; then
    echo "swift test HUNG: no exit within ${SWIFT_SUITE_TIMEOUT}s; the process tree was killed." >&2
    swift_suite_last_test "$log" >&2
    ok=1
  elif [[ "$rc" -ne 0 ]]; then
    echo "swift test exited $rc." >&2
    if grep $SWIFT_SUITE_GREP_OPTS -q 'unexpected signal code 6' "$log"; then
      echo "  signal 6: XCTest aborted the process on a stalled expectation, so the" >&2
      echo "  run stopped where it stood — see #1523." >&2
    fi
    swift_suite_last_test "$log" >&2
    ok=1
  fi

  if ! swift_suite_completed "$log"; then
    echo "swift test did not finish: the test bundle never reported its own result," >&2
    echo "  so the run was TRUNCATED and the suites after the last one shown never ran." >&2
    echo "  They report no failures because they did not execute, not because they passed." >&2
    swift_suite_last_test "$log" >&2
    ok=1
  elif ! swift_suite_ran_tests "$log"; then
    echo "swift test finished but executed 0 tests — the gate checked nothing." >&2
    ok=1
  fi

  return "$ok"
}

# swift_suite_run <log> <cmd...> — run <cmd...> under a pty, capturing combined
# output to <log>, and kill it if it outlives SWIFT_SUITE_TIMEOUT. Returns the
# command's exit status, or 124 if it had to be killed.
#
# `script -q /dev/null` is the pty. macOS-only spelling, which is fine because
# the only caller is a gate already guarded on Darwin, and it buys two things:
# XCTest line-buffers instead of block-buffering, so a hung run's log names the
# test it hung in rather than being empty; and the exit status is still the
# child's (verified — an aborting run comes back 1, a clean one 0).
swift_suite_run() {
  local log="${1:-}"; shift || true
  if [[ -z "$log" || $# -eq 0 ]]; then
    echo "swift_suite_run: needs <log> <cmd...>" >&2
    return 1
  fi
  : > "$log" || return 1

  # Job control so the child leads its own process group. `swift test` forks
  # the `xctest` process that does the actual hanging, and killing only the
  # wrapper leaves that child alive holding the pty — a "timeout" that does not
  # stop the thing it timed out on. With `set -m` the job's PGID equals its
  # PID, so one negative-PID kill reaches the whole tree.
  local had_monitor=0
  case "$-" in *m*) had_monitor=1 ;; esac
  set -m
  # stdin from /dev/null is load-bearing, not tidiness. BSD script(1) calls
  # tcgetattr on its OWN stdin before allocating the pty, and fails outright
  # ("tcgetattr/ioctl: Operation not supported on socket") whenever stdin is
  # not a terminal — which is every environment this gate actually runs in: a
  # CI step, a pre-push hook, an agent session. Without this the gate does not
  # merely lose the pty, it never starts the suite at all. Found by exactly
  # that failure while building this file.
  script -q /dev/null "$@" < /dev/null > "$log" 2>&1 &
  local child=$!
  [[ "$had_monitor" -eq 1 ]] || set +m

  local waited=0
  while kill -0 "$child" 2>/dev/null && (( waited < SWIFT_SUITE_TIMEOUT )); do
    sleep 1
    waited=$(( waited + 1 ))
  done

  if kill -0 "$child" 2>/dev/null; then
    kill -TERM -- "-$child" 2>/dev/null
    sleep 2
    kill -KILL -- "-$child" 2>/dev/null
    wait "$child" 2>/dev/null
    return 124
  fi

  wait "$child"
}
