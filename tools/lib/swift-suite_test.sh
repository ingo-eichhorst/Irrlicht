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
  # The grandchild's own lifetime and the deadline its reap is polled to below
  # are declared as a pair, and the relation between them is checked rather
  # than assumed (#1619). A poll that ran anywhere near ${gc_sleep}s would stop
  # asserting anything: "the fixture exited by itself" and "the tree was
  # reaped" would produce the same reading, and the case would pass vacuously.
  # This is the wall from ABOVE — the reap latency measured below gives a
  # deadline no lower bound worth speaking of, so without this it could be
  # raised to any number at all with nothing objecting.
  gc_sleep=300
  reap_deadline=15
  (( reap_deadline * 10 <= gc_sleep )) \
    || fail "the reap deadline (${reap_deadline}s) is not an order of magnitude under the grandchild's own lifetime (${gc_sleep}s) — at that ratio a natural exit starts reading as a reap"
  start=$(date +%s)
  SWIFT_SUITE_TIMEOUT=2 swift_suite_run "$log" \
    bash -c 'set -m; sleep '"$gc_sleep"' & echo $! > "$SWIFT_SUITE_GRANDCHILD_PIDFILE"; wait' >/dev/null 2>&1
  hang_rc=$?
  elapsed=$(( $(date +%s) - start ))
  [[ "$hang_rc" -eq 124 ]] && pass "a hanging command returns 124" || fail "hang should return 124, got $hang_rc"
  (( elapsed < 30 )) && pass "killed within the timeout (${elapsed}s)" || fail "took ${elapsed}s to give up"

  gc=$(cat "$SWIFT_SUITE_GRANDCHILD_PIDFILE" 2>/dev/null)
  if [[ -n "$gc" ]]; then
    # Polled, not slept (#1619). The first look happens with nothing in front
    # of it, so a tree already reaped when swift_suite_run returned — which is
    # what a correct implementation produces, since it posts SIGKILL and
    # `wait`s before returning — is reported at once instead of after a fixed
    # second. The deadline only absorbs a machine too busy to finish the
    # teardown; a tree that genuinely survives still fails loudly, now with the
    # elapsed time and the last state observed rather than a bare pid.
    #
    # `kill -0` is the predicate because it is a shell BUILTIN: it cannot fail
    # to run, so "we could not look" can never come out as "it is gone". `ps`
    # only DESCRIBES a pid that is still visible and never decides the verdict —
    # a missing or broken `ps` prints nothing, and nothing would otherwise read
    # as reaped.
    #
    # The residual failure mode #1619 names — the pid lingering as an unreaped
    # ZOMBIE, which `kill -0` cannot tell from a live process — is not
    # representable here, and a longer deadline would not have fixed it if it
    # were. Measured: the grandchild's parent is the inner `bash -c`, which is
    # itself in the victim list, and an orphaned zombie is reparented to launchd
    # and reaped before the first look (0 polls). A zombie needs a LIVE parent
    # that never waits, and this fixture has none. If one ever appeared anyway,
    # the failure line below carries `Z <defunct>` and says so itself.
    reap_start=$(date +%s)
    looks=0
    seen=""
    while :; do
      looks=$(( looks + 1 ))
      if ! kill -0 "$gc" 2>/dev/null; then seen=gone; break; fi
      psrow=$(ps -o stat=,command= -p "$gc" 2>/dev/null)
      if [[ -n "$psrow" ]]; then
        read -r gc_stat gc_cmd <<< "$psrow"
        seen="state $gc_stat, command ${gc_cmd:0:60}"
      else
        seen="visible to kill -0, but ps prints no row for it"
      fi
      (( $(date +%s) - reap_start >= reap_deadline )) && break
      sleep 0.1
    done
    reap_elapsed=$(( $(date +%s) - reap_start ))
    if [[ "$seen" == gone ]]; then
      # Printed rather than described in a comment: measured over 50 runs (25
      # idle, 25 at load average ~55 on 10 cores) this was always look 1 at 0s,
      # and a reap that started taking seconds would show up here instead of in
      # a doc comment nothing re-derives (#1572).
      pass "the whole process tree was reaped, not just the wrapper (gone on look $looks, after ${reap_elapsed}s)"
    else
      kill -9 "$gc" 2>/dev/null   # don't leak it out of the test either way
      fail "grandchild $gc survived the timeout kill — the process tree was not reaped (still there after ${reap_elapsed}s / $looks looks; last seen: $seen)"
    fi
  else
    fail "grandchild never recorded its pid; the kill assertion did not run"
  fi
  rm -f "$log" "$SWIFT_SUITE_GRANDCHILD_PIDFILE"

  # -------------------------------------------------------------------------
  # ...and all of that has to hold under the CALLER's `-e` (#1633).
  #
  # swift_suite_run is SOURCED, so it runs with whatever shell options its
  # caller happens to have, and its post-timeout sequence issues commands that
  # are EXPECTED to fail: after SIGTERM plus the 2s grace the victims are
  # already gone, so `kill -KILL` exits non-zero and `wait` reports the signal.
  # Under `-e` the shell dies on the first of those — before `return 124` — and
  # 124 is the single value swift_suite_verdict's entire hang diagnosis keys on.
  #
  # Two assertions in two DIFFERENT calling shapes, because bash's
  # errexit-context rule makes each one blind to the other's defect:
  #
  #   the 124   is read in a BARE statement position, the shape this file's
  #             header documents. Called from a `||` or an `if`, bash ignores
  #             -e for the whole function body, so the hazard cannot fire there
  #             and the assertion would pass against the unfixed lib.
  #   the leak  is read where a line after the call still runs, which under -e
  #             a bare 124 does not — the caller aborts on it (#1629, fixed on
  #             the caller side). A `||` position still EXECUTES any `set` the
  #             body performs, so that is exactly where a library which turned
  #             -e off and forgot to restore it becomes visible.
  echo "== swift_suite_run under the caller's \`-e\` (#1633) =="

  # `bash --noprofile --norc -e -o pipefail` is GitHub's own `shell: bash`
  # invocation, the same one calling_shape uses below. Everything the inner
  # shell needs arrives through the environment, so the heredocs are QUOTED and
  # carry no escaping to get wrong.
  e_log=$(mktemp -t swift-suite-e-log)
  e_pidfile=$(mktemp -t swift-suite-e-gc)
  e_opts_before=$(mktemp -t swift-suite-e-opts-before)
  e_opts_after=$(mktemp -t swift-suite-e-opts-after)
  export SWIFT_SUITE_E_LOG="$e_log" SWIFT_SUITE_E_PIDFILE="$e_pidfile" \
         SWIFT_SUITE_E_SLEEP="$gc_sleep" \
         SWIFT_SUITE_E_OPTS_BEFORE="$e_opts_before" SWIFT_SUITE_E_OPTS_AFTER="$e_opts_after"

  # The hang, driven exactly as the header documents it. Its return value is
  # read as the inner shell's EXIT STATUS, and that is the only place it can be
  # read: a correct 124 aborts the caller right there, so there is no line
  # after the call to print it from. errexit exits with the status of the
  # command that tripped it, so the inner shell's status IS what the function
  # returned — 124 once the timeout path runs to its end, and otherwise
  # whatever aborted it first (measured: 1 at `kill -KILL`, 143 at `wait`).
  errexit_hang_probe() {
    bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<'PROBE'
set -uo pipefail
. tools/lib/swift-suite.sh
SWIFT_SUITE_TIMEOUT=2 swift_suite_run "$SWIFT_SUITE_E_LOG" \
  bash -c 'set -m; sleep "$SWIFT_SUITE_E_SLEEP" & echo $! > "$SWIFT_SUITE_E_PIDFILE"; wait'
PROBE
  }
  e_hang_out=$(errexit_hang_probe); e_hang_rc=$?

  # A verification that could not run must not read as one that found nothing:
  # an inner shell that died before reaching the timeout path (a bad cwd, a
  # source that failed) exits 1, which is indistinguishable from the defect.
  # The fixture recording its pid is the proof that the path was reached.
  [[ -s "$e_pidfile" ]] \
    && pass "the -e probe reached the timeout path (its fixture recorded a pid)" \
    || fail "the -e probe never launched its fixture, so the assertion below measured something else; it printed: $e_hang_out"

  [[ "$e_hang_rc" -eq 124 ]] \
    && pass "a hanging command returns 124 under the caller's -e too" \
    || fail "under \`bash --noprofile --norc -e -o pipefail\` a hang came back $e_hang_rc, not 124 — the post-timeout kill sequence aborted the shell before \`return 124\`; it printed: $e_hang_out"

  e_gc=$(cat "$e_pidfile" 2>/dev/null)
  [[ -n "$e_gc" ]] && kill -9 "$e_gc" 2>/dev/null   # don't leak it out of a red run
  : > "$e_pidfile"

  # The other direction. Whatever swift_suite_run does about -e, it must not
  # pay for it by leaving the caller's options changed: a library that turns
  # errexit off and forgets to restore it is invisible for exactly the same
  # reason as the defect above, and does more damage.
  #
  # `set +o` REDIRECTED TO A FILE is the probe, and the spelling is
  # load-bearing rather than a style choice. `$(set +o)` runs in a command
  # substitution, and bash 3.2 — which is what /bin/bash is on macOS — reports
  # errexit and nounset as OFF inside one no matter what the parent has
  # (measured), so a probe built that way is byte-identical before and after
  # and can never see an errexit leak. `set` is a builtin, so redirecting it
  # does not fork. (`$-` is fork-free too, but has no flag character for
  # pipefail; the dump covers every `set -o` option there is.)
  leak_out=$(bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<'PROBE'
set -uo pipefail
. tools/lib/swift-suite.sh
set +o > "$SWIFT_SUITE_E_OPTS_BEFORE"
leak_rc=0
SWIFT_SUITE_TIMEOUT=2 swift_suite_run "$SWIFT_SUITE_E_LOG" \
  bash -c 'set -m; sleep "$SWIFT_SUITE_E_SLEEP" & echo $! > "$SWIFT_SUITE_E_PIDFILE"; wait' || leak_rc=$?
set +o > "$SWIFT_SUITE_E_OPTS_AFTER"
echo "leak probe: swift_suite_run returned $leak_rc"
PROBE
)
  if [[ ! -s "$e_opts_before" || ! -s "$e_opts_after" ]]; then
    fail "the option-leak probe recorded no before/after option dump — it could not run; it printed: $leak_out"
  elif diff "$e_opts_before" "$e_opts_after" >/dev/null 2>&1; then
    pass "the caller's shell options are unchanged across the call"
  else
    fail "swift_suite_run LEAKED a shell option change back to its caller:
$(diff "$e_opts_before" "$e_opts_after" | sed 's/^/    /')"
  fi

  e_gc=$(cat "$e_pidfile" 2>/dev/null)
  [[ -n "$e_gc" ]] && kill -9 "$e_gc" 2>/dev/null
  rm -f "$e_log" "$e_pidfile" "$e_opts_before" "$e_opts_after"
fi

# ---------------------------------------------------------------------------
# The CALLING shape, under the shell GitHub actually runs steps with (#1629).
#
# Everything above tests swift_suite_verdict's JUDGEMENT. The two cases below
# test whether it gets to speak at all — a property of the caller, and one that
# was false in CI for the whole of macos-swift.yml's life. GitHub invokes
# `shell: bash` as `bash --noprofile --norc -e -o pipefail {0}`, so `-e` is
# ALREADY on, the step's own `set -uo pipefail` does not clear it, and the
# failing subshell aborted the step before `rc=$?` could run. A hang and an
# ordinary assertion failure therefore printed the same thing —
# `Process completed with exit code 1` — which is the exact ambiguity this
# whole file exists to remove.
#
# Deliberately NOT a general workflow linter, and the scope is the honest part.
# Measured against this repo's three occurrences of the hazard, a check keyed
# on `$?` would have caught two: the third (a `for` loop over every installed
# Xcode that died on its second iteration) captures no `$?` anywhere, and
# neither does a cleanup line after a command allowed to fail. The majority of
# the hazard has no mechanical signature, so a linter's green would claim
# coverage it does not have. This is a lock on the ONE call site that exists.
echo "== the calling shape under GitHub's \`shell: bash\` (#1629) =="

# macos-swift.yml's step reduced to its decisive lines, pointed at a committed
# fixture so the real verdict function is what does or does not speak.
calling_shape() {
  local guard=""
  [[ "$1" == guarded ]] && guard="set +e"
  bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<EOF
$guard
set -uo pipefail
. tools/lib/swift-suite.sh
( exit 124 )
rc=\$?
swift_suite_verdict "\$rc" "$DATA/hung.log"
EOF
}

# Captured into a variable rather than piped into grep, and that is not style.
# This file runs under `set -o pipefail`, and `calling_shape` legitimately exits
# non-zero (the verdict returns 1 for a hang) — so `calling_shape … | grep -q`
# reports NOMATCH even when grep matched, because pipefail hands back the
# left-hand status. Caught exactly that way while writing this, which is the
# same shell-option-you-cannot-see family as the defect under test.
bare_out=$(calling_shape bare)
guarded_out=$(calling_shape guarded)

# The vacuity guard, and it is the load-bearing half of the pair: if bash (or a
# future runner spelling) ever stopped aborting here, the `set +e` pinned below
# would be protecting nothing and the guarded case would pass for the wrong
# reason. Absence of the hazard and absence of the check must not look alike.
if grep -aq 'HUNG' <<<"$bare_out"; then
  fail "bare shape: the verdict spoke under -e — the hazard this pins is gone, so re-derive the fix rather than trusting it"
else
  pass "bare shape: -e swallows the verdict before rc=\$? runs (the defect, still real)"
fi

if grep -aq 'HUNG: no exit within' <<<"$guarded_out"; then
  pass "guarded shape: \`set +e\` lets swift_suite_verdict diagnose the hang"
else
  fail "guarded shape: \`set +e\` did not restore the verdict; it printed:
$guarded_out"
fi

# ...and the workflow actually carries that line, in EVERY step that reads `$?`.
#
# `|| rc=$?` is accepted alongside `set +e` because it is the other spelling
# that keeps the code; `|| true` is deliberately NOT accepted, because it lets
# the capture run but leaves `$?` holding `true`'s status, silently reporting
# every failing run as 0. Measured all three ways.
WF=.github/workflows/macos-swift.yml
if [[ ! -f "$WF" ]]; then
  fail "$WF not found — the call-site check could not run"
else
  shape=$(awk '
    function flush() {
      if (inblock && cap) {
        seen++
        if (!guard) { printf "  offending run: block at %s:%d\n", FILENAME, blockline; bad++ }
      }
      inblock=0; cap=0; guard=0
    }
    {
      match($0, /^ */); ind = RLENGTH
      if (inblock && $0 !~ /^[[:space:]]*$/ && ind <= runind) flush()
      if (!inblock && $0 ~ /^[[:space:]]*run:[[:space:]]*\|/) {
        inblock=1; runind=ind; blockline=NR; next
      }
      if (inblock) {
        stripped = $0; sub(/^[[:space:]]*/, "", stripped)
        if (stripped ~ /^#/) next
        if (stripped ~ /^set \+e([[:space:]]|$)/) guard=1
        # An assignment from $? that is NOT the right-hand side of a `||`.
        if (stripped ~ /[A-Za-z_][A-Za-z0-9_]*=\$\?/ &&
            stripped !~ /\|\|[[:space:]]*[A-Za-z_][A-Za-z0-9_]*=\$\?/) cap=1
      }
    }
    END { flush(); printf "SEEN %d\nBAD %d\n", seen+0, bad+0 }
  ' "$WF")
  seen_caps=$(sed -n 's/^SEEN //p' <<<"$shape")
  bad_caps=$(sed -n 's/^BAD //p' <<<"$shape")

  # Vacuity guard: a parse that stopped finding the blocks reads exactly like a
  # workflow with no hazard in it. Two is what the file carries today
  # (swift-test and swift-snapshot-evidence); a THIRD is fine, zero or one is
  # the check having gone blind.
  if [[ "${seen_caps:-0}" -lt 2 ]]; then
    fail "found only ${seen_caps:-0} run: block(s) capturing \$? in $WF — expected at least 2; the scan has gone blind, not the workflow clean"
  else
    pass "scan reads $WF: $seen_caps run: block(s) capture \$?"
    if [[ "${bad_caps:-1}" -eq 0 ]]; then
      pass "every one of them disarms -e first (\`set +e\`) — swift_suite_verdict can run"
    else
      fail "$bad_caps run: block(s) read \$? under GitHub's -e, so the capture is unreachable:
$(grep -a 'offending run: block' <<<"$shape")"
    fi
  fi
fi

echo "== real-home witness (#1669, #1670) =="
# Every case here runs against a FIXTURE home under $TMPDIR, never the real
# one. That is not politeness: planting a file in the developer's home to prove
# a guard against planting files in the developer's home would reproduce the
# incident this guard exists for. `SWIFT_SUITE_WITNESS_HOME` exists for exactly
# this caller and for no other.
#
# The `want 0` rows carry as much of the value as the rest: a witness that
# reported unconditionally would satisfy every mutation below and read as
# excellent coverage.

# _witness_home — a fixture home shaped like a real one: Preferences present
# and non-empty (so the vacuity guard is satisfied), Sounds absent (so the
# "the run created it" case is reachable).
_witness_home() {
  local home
  home=$(mktemp -d -t swift-suite-witness-home) || return 1
  mkdir -p "$home/Library/Preferences" || return 1
  printf 'x' > "$home/Library/Preferences/com.example.settled.plist" || return 1
  printf '%s\n' "$home"
}

# _witness_scrub <dir> — remove a directory THIS FILE created, and refuse
# anything else. The refusal is the point: it is a one-line reproduction of the
# rule that a delete path must never be able to address a directory it did not
# create.
_witness_scrub() {
  case "$1" in
    "${TMPDIR:-/tmp}"swift-suite-witness-*|/tmp/swift-suite-witness-*|/var/folders/*/swift-suite-witness-*)
      rm -rf "$1" ;;
    *) echo "  refusing to remove '$1' — not a fixture this test created" >&2; return 1 ;;
  esac
}

# _witness_case <name> <want-rc> <mutation-fn> [expected-substring]
_witness_case() {
  local name="$1" want="$2" mutate="$3" needle="${4:-}" home state out got
  home=$(_witness_home) || { fail "$name: could not build the fixture home"; return; }
  state=$(mktemp -d -t swift-suite-witness-state) || { fail "$name: no state dir"; return; }

  SWIFT_SUITE_WITNESS_HOME="$home" swift_suite_witness_before "$state"
  "$mutate" "$home"
  out=$(SWIFT_SUITE_WITNESS_HOME="$home" swift_suite_witness_verdict "$state" 2>&1)
  got=$?

  if [[ "$got" == "$want" ]]; then
    pass "$name → rc $got"
  else
    fail "$name: expected rc $want, got $got; output: $out"
  fi
  if [[ -n "$needle" ]]; then
    case "$out" in
      *"$needle"*) pass "$name: names '$needle'" ;;
      *) fail "$name: output does not name '$needle'; got: $out" ;;
    esac
  fi
  chmod -R u+rwX "$home" 2>/dev/null
  _witness_scrub "$home"
  _witness_scrub "$state"
}

_witness_noop() { :; }
_witness_plant() { printf 'leak' > "$1/Library/Preferences/com.irrlicht.leaked-by-a-test.plist"; }
_witness_remove_entry() { rm -f "$1/Library/Preferences/com.example.settled.plist"; }
_witness_create_sounds() { mkdir -p "$1/Library/Sounds"; }
_witness_install_sound() { mkdir -p "$1/Library/Sounds"; printf 'a' > "$1/Library/Sounds/IrrlichtCustom-ready.aiff"; }
_witness_remove_dir() { rm -rf "$1/Library/Preferences"; }
_witness_unreadable() { chmod 000 "$1/Library/Preferences"; }
_witness_empty_home() { rm -rf "$1/Library"; }

# The vacuity guard. Without it, every row below is satisfied by a witness that
# always fails.
_witness_case "a run that changed nothing passes" 0 _witness_noop "no new entries"
# #1661's shape: a file appears in ~/Library/Preferences.
_witness_case "a planted preference file is caught" 1 _witness_plant "com.irrlicht.leaked-by-a-test.plist"
# #1670's shape, both halves.
_witness_case "creating ~/Library/Sounds is caught" 1 _witness_create_sounds "CREATED"
_witness_case "an installed sound is caught" 1 _witness_install_sound "IrrlichtCustom-ready.aiff"
# The direction nothing in this repo is allowed to move in at all.
_witness_case "a removed entry is caught" 1 _witness_remove_entry "REMOVED"
_witness_case "a removed watched directory is caught" 1 _witness_remove_dir "gone now"
# "Could not look" and "found nothing" must not print the same thing. Both of
# these would be a clean report from a witness that simply returned early.
_witness_case "an unreadable watched directory fails loudly" 1 _witness_unreadable "unwitnessed"
_witness_case "a home with nothing to watch fails loudly" 1 _witness_empty_home "not one watched directory"

# A verdict with no `before` snapshot is the same class: the run was not
# witnessed, and saying nothing would mean "nothing was measured".
_no_before=$(mktemp -d -t swift-suite-witness-state)
_out=$(swift_suite_witness_verdict "$_no_before" 2>&1) && \
  fail "a verdict with no before-snapshot must fail" || pass "no before-snapshot → fail"
case "$_out" in
  *"NOT witnessed"*) pass "an unwitnessed run says so" ;;
  *) fail "an unwitnessed run must say so; got: $_out" ;;
esac
_witness_scrub "$_no_before"
swift_suite_witness_verdict "" >/dev/null 2>&1 && fail "missing statedir must fail" || pass "missing statedir → fail"

# The witness reads the PASSWORD DATABASE, not $HOME. A witness that followed
# $HOME would watch an empty temp directory and report clean forever — and
# moving $HOME is one of the things a future fix in this area might do.
_real_home=$(swift_suite_witness_home)
_moved_home=$(HOME=/tmp/definitely-not-the-home swift_suite_witness_home)
[[ -n "$_real_home" && "$_real_home" == "$_moved_home" ]] \
  && pass "swift_suite_witness_home ignores \$HOME ($_real_home)" \
  || fail "swift_suite_witness_home followed \$HOME: '$_real_home' vs '$_moved_home'"
[[ -d "$_real_home" ]] && pass "the home it reads is a directory" \
  || fail "swift_suite_witness_home returned '$_real_home', which is not a directory"

# The watched set is asserted rather than left implicit: it is the whole scope
# of the guard, and silently shrinking it to nothing is indistinguishable from
# a guard that finds nothing.
[[ "${#SWIFT_SUITE_WITNESSED_DIRS[@]}" -ge 2 ]] \
  && pass "watching ${#SWIFT_SUITE_WITNESSED_DIRS[@]} directories: ${SWIFT_SUITE_WITNESSED_DIRS[*]}" \
  || fail "the watched set has shrunk to ${#SWIFT_SUITE_WITNESSED_DIRS[@]} — the guard covers almost nothing"

if [[ "$rc" -eq 0 ]]; then echo "swift-suite_test: ALL PASS"; else echo "swift-suite_test: FAILURES" >&2; fi
exit "$rc"
