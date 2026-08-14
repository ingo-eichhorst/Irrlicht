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
# reason: a job stuck `in_progress` reads as "still queued". It mirrors that
# rationale, not that workflow's number (15 minutes, for a ~45s build).
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

# swift_suite_bundle_failed <log> — did the bundle report its own result as
# FAILED? Read independently of the exit code, which is the only other source
# of "did it pass" and reaches us through script(1)'s status propagation. That
# propagation is a single point of failure and is spelled differently on Linux
# (util-linux script needs -e to return the child's status at all), so a port
# that got it wrong would otherwise read every failing suite as a pass.
swift_suite_bundle_failed() {
  grep $SWIFT_SUITE_GREP_OPTS -qE "Test Suite '.*\.xctest' failed at" "$1"
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

# _swift_suite_hung_headline — written from two branches (a hang that printed
# something, and one that printed nothing), which is exactly the kind of pair
# that drifts apart.
_swift_suite_hung_headline() {
  echo "swift test HUNG: no exit within ${SWIFT_SUITE_TIMEOUT}s; the process tree was killed." >&2
}

# swift_suite_verdict <exit-code> <log> — 0 only if the run both passed and
# demonstrably finished. Prints a diagnosis naming which of the two failed.
swift_suite_verdict() {
  local rc="${1:-}" log="${2:-}" ok=0

  if [[ -z "$rc" || -z "$log" ]]; then
    echo "swift_suite_verdict: needs <exit-code> <log>" >&2
    return 1
  fi
  # Numeric, checked rather than assumed: bash's arithmetic context resolves a
  # bare word as a variable name, so `[[ abc -eq 124 ]]` compares 0 and a
  # garbled exit code would silently take the success path.
  if [[ ! "$rc" =~ ^[0-9]+$ ]]; then
    echo "swift_suite_verdict: exit code '$rc' is not a number — refusing to judge the run." >&2
    return 1
  fi
  if [[ ! -f "$log" ]]; then
    echo "swift test produced no log at '$log' — treating the run as failed." >&2
    return 1
  fi
  # An EMPTY log is a different diagnosis from a truncated one and deserves its
  # own line: nothing started, rather than something starting and stopping
  # partway. Reporting it as "truncated" sends the reader looking for a test
  # that never ran.
  #
  # The hang case is checked FIRST inside this branch, and the order is the
  # decision: a run killed at the timeout having printed nothing is still a
  # hang, and calling it "never started" would send the reader to the pty and
  # the toolchain when the process was in fact alive the whole time.
  if [[ ! -s "$log" ]]; then
    if [[ "$rc" -eq 124 ]]; then
      _swift_suite_hung_headline
      echo "  It produced no output at all, so there is no test name to attribute it to —" >&2
      echo "  the hang is before or during startup rather than inside a test." >&2
    else
      echo "swift test produced NO output at all — the run never started." >&2
      echo "  A pty that could not be allocated looks exactly like this; so does a" >&2
      echo "  toolchain that failed to launch. Re-run, and check \`script -q /dev/null true\`." >&2
    fi
    return 1
  fi

  if [[ "$rc" -eq 124 ]]; then
    _swift_suite_hung_headline
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

  # Asked separately from the exit code, on purpose — see swift_suite_bundle_failed.
  if swift_suite_bundle_failed "$log"; then
    echo "swift test: the test bundle reported its own result as FAILED." >&2
    ok=1
  fi

  return "$ok"
}

# _swift_suite_descendants <pid> — <pid> and every descendant, deepest first.
#
# Collected BEFORE anything is signalled, which is the whole trick: once the
# wrapper dies its children reparent to launchd and the ppid chain that finds
# them is gone. Deepest-first so a parent cannot spawn a replacement between
# two kills.
_swift_suite_descendants() {
  local kid
  for kid in $(pgrep -P "$1" 2>/dev/null); do
    _swift_suite_descendants "$kid"
  done
  echo "$1"
}

# swift_suite_run <log> <cmd...> — run <cmd...> under a pty, streaming its
# output AND capturing it to <log>, and kill it if it outlives
# SWIFT_SUITE_TIMEOUT. Returns the command's exit status, or 124 if it had to
# be killed.
#
# `script -q "$log"` is the pty. macOS-only spelling, which is fine because the
# only caller is a gate already guarded on Darwin, and it buys three things:
# XCTest line-buffers instead of block-buffering, so a hung run's log names the
# test it hung in rather than being empty; the output still reaches the
# terminal live, so a gate that is going to hang for minutes is not also
# silent; and the exit status is still the child's (verified — an aborting run
# comes back 1, a clean one 0, a SIGKILLed one 9).
swift_suite_run() {
  local log="${1:-}"; shift || true
  if [[ -z "$log" || $# -eq 0 ]]; then
    echo "swift_suite_run: needs <log> <cmd...>" >&2
    return 1
  fi
  : > "$log" || return 1

  # stdin from /dev/null is load-bearing, not tidiness. BSD script(1) calls
  # tcgetattr on its OWN stdin before allocating the pty, and fails outright
  # ("tcgetattr/ioctl: Operation not supported on socket") whenever stdin is
  # not a terminal — which is every environment this gate actually runs in: a
  # CI step, a pre-push hook, an agent session. Without this the gate does not
  # merely lose the pty, it never starts the suite at all.
  script -q "$log" "$@" < /dev/null 2>&1 &
  local child=$!

  local waited=0
  while kill -0 "$child" 2>/dev/null && (( waited < SWIFT_SUITE_TIMEOUT )); do
    sleep 1
    waited=$(( waited + 1 ))
  done

  if kill -0 "$child" 2>/dev/null; then
    # Kill the tree by explicit pid, NOT by process group. script(1) calls
    # login_tty, so `swift test` and the `xctest` it forks live in a different
    # SESSION and different process groups from the wrapper: a negative-PID
    # kill on the wrapper's group reaches the wrapper alone. That looks like it
    # works, because closing the pty master takes a *chatty* child down via
    # SIGPIPE — and a genuinely hung one writes nothing, so it survives, keeps
    # the pty and the SwiftPM build lock, and accumulates across runs. Measured
    # both ways; tools/lib/swift-suite_test.sh pins it with a fixture that is
    # silent and in its own process group.
    local victims
    victims=$(_swift_suite_descendants "$child")
    # The braces-with-stderr-suppressed wrapper is for legibility, not safety:
    # the shell reports a killed background job as
    # "swift-suite.sh: line NN: 1234 Terminated: 15  script -q ..." when it
    # reaps it, which lands immediately before the hang diagnosis and reads as
    # an error *in this file* at exactly the moment the gate is trying to
    # explain itself. The notice is written when `wait` reaps, so the
    # redirection has to cover the wait, not just the kills.
    {
      # Unquoted on purpose: a whitespace-separated pid list.
      # shellcheck disable=SC2086
      kill -TERM $victims 2>/dev/null
      sleep 2
      # shellcheck disable=SC2086
      kill -KILL $victims 2>/dev/null
      wait "$child"
    } 2>/dev/null
    return 124
  fi

  wait "$child"
}
