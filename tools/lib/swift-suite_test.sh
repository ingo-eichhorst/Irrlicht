#!/usr/bin/env bash
# swift-suite_test.sh — tests for tools/lib/swift-suite.sh (#1523).
#
# The corpus under testdata/swift-suite/ is committed rather than improvised so
# the shapes outlive this PR. Each fixture is a real observed outcome reduced to
# its decisive lines:
#
#   clean.log          a complete run  — bundle reports, 311 tests
#   aborted.log        XCTest's stall detector fired: signal 6, bundle silent
#   hung.log           the process never returned: bundle silent, no signal
#   zero-tests.log     bundle reports, but executed nothing
#   control-bytes.log  a complete run carrying the control bytes a real run emits
#   bundle-failed.log  a complete run whose bundle reports FAILED
#
# aborted.log and hung.log both still contain per-suite `Executed N tests`
# lines, which is the point: that string is present in a truncated run, so a
# completeness check keyed on it passes over exactly the failure it exists to
# catch. Measured on the real logs behind these fixtures, a truncated run
# carried 29-33 of them.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: cannot cd to repo root $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to preflight's shell_lib_tests (`bash "$t" || rc=1`), so the gate would
# go green having asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: swift-suite_test — $1 not found" >&2; exit 1; }; }
need grep
need git

. tools/lib/swift-suite.sh

DATA=tools/lib/testdata/swift-suite
rc=0
pass() { echo "  ok   — $1"; }
fail() { echo "  FAIL — $1" >&2; rc=1; }

# The corpus must actually contain what it claims. A fixture tree that quietly
# stopped carrying its own cases reads as a pass.
for f in clean aborted hung zero-tests control-bytes bundle-failed; do
  [[ -s "$DATA/$f.log" ]] || fail "fixture $DATA/$f.log missing or empty"
  # Tracked, not merely present. The repo-root .gitignore carries a blanket
  # `*.log`, so these fixtures exist on the author's disk and reach nobody
  # else's — a corpus that silently stops containing its own cases. Caught
  # exactly that way; testdata/swift-suite/.gitignore is the fix.
  git ls-files --error-unmatch "$DATA/$f.log" >/dev/null 2>&1 \
    || fail "fixture $DATA/$f.log is not tracked by git — see $DATA/.gitignore"
done
grep -aq 'unexpected signal code 6' "$DATA/aborted.log" || fail "aborted.log no longer carries the signal-6 line"
grep -aqE 'Executed [1-9][0-9]* test' "$DATA/aborted.log" || \
  fail "aborted.log must still carry per-suite aggregates — that is what makes it a trap"
grep -aqE 'Executed [1-9][0-9]* test' "$DATA/hung.log" && \
  fail "hung.log unexpectedly carries an aggregate"
LC_ALL=C grep -q $'\000' "$DATA/control-bytes.log" || \
  fail "control-bytes.log no longer contains a NUL byte — the -a case is vacuous without one"

echo "== swift_suite_completed =="
for f in clean control-bytes zero-tests; do
  swift_suite_completed "$DATA/$f.log" && pass "$f: bundle reported" || fail "$f: should read as completed"
done
for f in aborted hung; do
  swift_suite_completed "$DATA/$f.log" && fail "$f: must NOT read as completed" || pass "$f: truncation detected"
done

echo "== swift_suite_ran_tests =="
swift_suite_ran_tests "$DATA/clean.log" && pass "clean: ran tests" || fail "clean: should have run tests"
swift_suite_ran_tests "$DATA/zero-tests.log" && fail "zero-tests: must not count as having run" || pass "zero-tests: vacuous run detected"

echo "== swift_suite_verdict =="
verdict() { swift_suite_verdict "$1" "$DATA/$2.log" >/dev/null 2>&1; echo $?; }

[[ "$(verdict 0 clean)" == 0 ]] && pass "exit 0 + complete log → pass (vacuity guard)" \
  || fail "a healthy run must pass, else every other case here is meaningless"
[[ "$(verdict 1 aborted)" == 1 ]] && pass "exit 1 + aborted log → fail" || fail "aborted run must fail"
[[ "$(verdict 124 hung)" == 1 ]] && pass "exit 124 + hung log → fail" || fail "hung run must fail"
# The decisive one: the exit code says success and the run was still truncated.
# This is the case #1523 is about, and the only check that catches it is the
# completeness one — an exit-code gate passes it.
[[ "$(verdict 0 hung)" == 1 ]] && pass "exit 0 + TRUNCATED log → fail (exit code alone would pass)" \
  || fail "a truncated run with exit 0 must fail — this is the whole point"
[[ "$(verdict 0 zero-tests)" == 1 ]] && pass "exit 0 + 0 tests → fail" || fail "a run that executed nothing must fail"
# The ugrep case: on this repo's developer machines `grep` is ugrep, which
# classifies a NUL-bearing stream as binary and matches NOTHING without -a. A
# verdict built on a bare grep reports this complete run as truncated.
[[ "$(verdict 0 control-bytes)" == 0 ]] && pass "control bytes: complete run still reads as complete" \
  || fail "control-byte log misread — swift_suite_* lost its -a"
# The bundle's own verdict is read independently of the exit code, so a
# script(1) that stopped propagating the child's status cannot turn a failing
# suite into a pass.
[[ "$(verdict 0 bundle-failed)" == 1 ]] && pass "exit 0 + bundle reported FAILED → fail" \
  || fail "a bundle that reported FAILED must fail even at exit 0"
# A non-numeric exit code must be refused, not silently compared as 0.
swift_suite_verdict abc "$DATA/clean.log" >/dev/null 2>&1 \
  && fail "a non-numeric exit code must be refused" || pass "non-numeric exit code → fail"
swift_suite_verdict 0 "$DATA/nope.log" >/dev/null 2>&1 && fail "a missing log must fail" || pass "missing log → fail"
swift_suite_verdict "" "" >/dev/null 2>&1 && fail "missing args must fail" || pass "missing args → fail"
# An empty log is "never started", not "truncated" — generated rather than
# committed because the corpus guard above requires every fixture to be
# non-empty, which is the property this one case has to violate.
empty_log=$(mktemp -t swift-suite-empty) && : > "$empty_log"
swift_suite_verdict 0 "$empty_log" >/dev/null 2>&1 && fail "an empty log must fail" || pass "empty log → fail"
# Captured rather than piped: `set -o pipefail` is on, and swift_suite_verdict
# returns 1 here by design, so a pipeline would report the grep as failed no
# matter what it matched.
empty_msg=$(swift_suite_verdict 0 "$empty_log" 2>&1 || true)
case "$empty_msg" in
  *"never started"*) pass "empty log is diagnosed as never-started, not as truncated" ;;
  *) fail "empty log should be diagnosed distinctly from truncation; got: $empty_msg" ;;
esac
# ...and an empty log at exit 124 is a HANG that printed nothing, not a run
# that never started. Getting this backwards points the reader at the pty and
# the toolchain when the process was alive the whole time.
hang_msg=$(swift_suite_verdict 124 "$empty_log" 2>&1 || true)
case "$hang_msg" in
  *HUNG*)          pass "empty log at exit 124 is diagnosed as a hang" ;;
  *)               fail "empty log at 124 should read as a hang; got: $hang_msg" ;;
esac
case "$hang_msg" in
  *"never started"*) fail "a hang must not be reported as never-started" ;;
  *)                 pass "a hang is not mislabelled never-started" ;;
esac
rm -f "$empty_log"

echo "== SWIFT_SUITE_TIMEOUT default is reachable =="
# #1530. Every diagnosis in this file is unreachable if the bound outlives the
# caller waiting for it: at the old default of 600s an automated caller's Bash
# tool call — also 600s — was killed at the same instant the gate would have
# started speaking, so the HUNG branch could never print for the caller that
# most needs it. The gate runs `swift build` first, so what has to fit inside
# the pre-push budget is the bound PLUS a cold build.
#
# Read from a subshell with the variable unset, so this measures the DEFAULT
# and not whatever the surrounding run exported.
default_timeout=$(unset SWIFT_SUITE_TIMEOUT; . tools/lib/swift-suite.sh; echo "$SWIFT_SUITE_TIMEOUT")
[[ "$default_timeout" =~ ^[0-9]+$ ]] \
  && pass "default timeout is numeric ($default_timeout s)" \
  || fail "default timeout is not a number: '$default_timeout'"
if (( default_timeout + SWIFT_SUITE_COLD_BUILD_SECONDS < SWIFT_SUITE_MIN_HEADROOM )); then
  pass "default ${default_timeout}s + ${SWIFT_SUITE_COLD_BUILD_SECONDS}s cold build fits in ${SWIFT_SUITE_MIN_HEADROOM}s"
else
  fail "default ${default_timeout}s + ${SWIFT_SUITE_COLD_BUILD_SECONDS}s cold build does NOT fit in ${SWIFT_SUITE_MIN_HEADROOM}s — the HUNG diagnosis cannot print for a bounded caller"
fi

echo "== swift_suite_last_test =="
swift_suite_last_test "$DATA/hung.log" | grep -aq 'testRoleOrchestratorRow' \
  && pass "names the test a hung run stopped in" || fail "should name the last started test"

echo "== swift_suite_run =="
if [[ "$(uname -s)" != "Darwin" ]]; then
  # Announced, never silent: swift_suite_run wraps BSD script(1), whose
  # argument order differs on Linux, and the only caller is a Darwin-guarded
  # gate. Saying so is the difference between "out of scope here" and "untested".
  echo "  SKIP — swift_suite_run needs BSD script(1); this host is $(uname -s). Covered on macOS."
else
  need script
  # Precondition, checked rather than assumed: every assertion below runs a
  # command through a pty, so a host that cannot allocate one fails all of them
  # and reads as a defect in the lib rather than as an environment problem.
  # Note the `< /dev/null`: script(1) tcgetattr's its own stdin and fails when
  # that is a socket or pipe, which is what a CI step and an agent session both
  # hand it. That is the same redirect swift_suite_run makes, so this really is
  # checking the configuration the lib uses. Retried once, then a loud failure —
  # never a silent skip.
  pty_ok=0
  for _ in 1 2; do
    if script -q /dev/null true < /dev/null >/dev/null 2>&1; then pty_ok=1; break; fi
    sleep 1
  done
  [[ "$pty_ok" -eq 1 ]] || fail "cannot allocate a pty on this host (script -q /dev/null true fails) — swift_suite_run is untested"

  log=$(mktemp -t swift-suite-test) || fail "mktemp failed"

  swift_suite_run "$log" bash -c 'exit 0' >/dev/null 2>&1
  [[ $? -eq 0 ]] && pass "returns 0 for a passing command" || fail "should have returned 0"

  swift_suite_run "$log" bash -c 'exit 3' >/dev/null 2>&1
  [[ $? -eq 3 ]] && pass "propagates a non-zero exit status" || fail "should have returned 3"

  swift_suite_run "$log" bash -c 'echo marker-line' >/dev/null 2>&1
  grep -aq 'marker-line' "$log" && pass "captures output to the log" || fail "log should carry the output"

  # A hang must be killed within the timeout AND take its grandchildren with
  # it: `swift test` forks the process that actually hangs, so a kill that
  # reaches only the wrapper is a timeout that does not stop anything.
  #
  # The fixture has to occupy `xctest`'s ACTUAL position, and two details of
  # that are load-bearing — an earlier version of this test had neither and
  # passed against a wrapper-only kill, i.e. against no mechanism at all:
  #
  #   `set -m`  puts the grandchild in its OWN process group, as SwiftPM's
  #             xctest is. Sharing the inner shell's group instead means the
  #             pty hangup reaps it and the assertion measures SIGHUP.
  #   silence   a hung process writes nothing. A chatty one dies of SIGPIPE
  #             when the pty master closes, which again is not the kill.
  #
  # script(1) calls login_tty, so everything below it is in a different SESSION
  # from this shell — which is exactly why a negative-PID kill on the wrapper's
  # process group cannot reach it.
  export SWIFT_SUITE_GRANDCHILD_PIDFILE
  SWIFT_SUITE_GRANDCHILD_PIDFILE=$(mktemp -t swift-suite-gc)
  start=$(date +%s)
  SWIFT_SUITE_TIMEOUT=2 swift_suite_run "$log" \
    bash -c 'set -m; sleep 300 & echo $! > "$SWIFT_SUITE_GRANDCHILD_PIDFILE"; wait' >/dev/null 2>&1
  hang_rc=$?
  elapsed=$(( $(date +%s) - start ))
  [[ "$hang_rc" -eq 124 ]] && pass "a hanging command returns 124" || fail "hang should return 124, got $hang_rc"
  (( elapsed < 30 )) && pass "killed within the timeout (${elapsed}s)" || fail "took ${elapsed}s to give up"

  gc=$(cat "$SWIFT_SUITE_GRANDCHILD_PIDFILE" 2>/dev/null)
  if [[ -n "$gc" ]]; then
    sleep 1
    if kill -0 "$gc" 2>/dev/null; then
      kill -9 "$gc" 2>/dev/null   # don't leak it out of the test either way
      fail "grandchild $gc survived the timeout kill — the process tree was not reaped"
    else
      pass "the whole process tree was reaped, not just the wrapper"
    fi
  else
    fail "grandchild never recorded its pid; the kill assertion did not run"
  fi
  rm -f "$log" "$SWIFT_SUITE_GRANDCHILD_PIDFILE"
fi

if [[ "$rc" -eq 0 ]]; then echo "swift-suite_test: ALL PASS"; else echo "swift-suite_test: FAILURES" >&2; fi
exit "$rc"
