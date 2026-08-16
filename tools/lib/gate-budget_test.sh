#!/usr/bin/env bash
# gate-budget_test.sh — unit tests for lib/gate-budget.sh plus one end-to-end
# check that tools/preflight.sh actually wires it. Plain bash, no framework,
# matching tools/lib/changed-files_test.sh. Run directly, or via
# tools/preflight.sh's `tools` gate.
#
# Covers #1570. The bug being fixed is a SILENCE — the pre-push hook exceeding
# an automated caller's 600s command budget and being killed from outside with
# no gate name, no summary and no exit code — so the tests that matter are the
# ones asserting the run says which gate ran out of time and which never
# started. A budget that expired without naming anything would reproduce the
# defect inside its own fix.
#
# The end-to-end block at the bottom is the mutation evidence, and it is a
# mutation of a COPY: preflight.sh is duplicated to a scratch name inside
# tools/ (so its own `dirname $0/..` root resolution still lands on the repo)
# with one gate's command replaced by a sleep. Nothing tracked is written, so
# an interrupted run cannot leave the tree broken. The substitution is asserted
# to have applied — #1390's lesson: a mutation harness whose mutation silently
# no-ops reports the unmutated result as evidence.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
# shellcheck source=gate-budget.sh
source "$DIR/gate-budget.sh"

# Keep the suite in seconds rather than tens of seconds. The grace period is
# how long budget_run waits between SIGTERM and SIGKILL; 1s is plenty for the
# `sleep` processes below and does not change what is being tested.
# shellcheck disable=SC2034  # both are read by gate-budget.sh, sourced above
BUDGET_TERM_GRACE_SECONDS=1
# shellcheck disable=SC2034
BUDGET_POLL_SECONDS=0.05

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  [[ "$2" == "$3" ]] && pass "$1" || fail "$1" "$2" "$3"
  return 0
}
assert_contains() {
  case "$3" in
    *"$2"*) pass "$1" ;;
    *) fail "$1" "output containing: $2" "$3" ;;
  esac
  return 0
}
# assert_le <label> <ceiling> <actual>
assert_le() {
  [[ "$3" -le "$2" ]] && pass "$1 ($3 <= $2)" || fail "$1" "<= $2" "$3"
  return 0
}

echo "== budget_open refuses anything that is not a whole number of seconds =="
# A bound that quietly is not one is worse than no bound: the caller stops
# watching for the outcome it believes it is protected from.
for bad in "10m" "-5" "1.5" "abc" ""; do
  budget_open "$bad" >/dev/null 2>&1
  assert_eq "budget_open '$bad' → 2" "2" "$?"
done
budget_open 0 >/dev/null 2>&1
assert_eq "budget_open 0 → 0 (unbounded is legal)" "0" "$?"

echo ""
echo "== an unbounded budget is never bounded and never exhausted =="
budget_open 0
if budget_is_bounded; then bounded=yes; else bounded=no; fi
assert_eq "budget_open 0 → not bounded" "no" "$bounded"
if budget_exhausted; then ex=yes; else ex=no; fi
assert_eq "budget_open 0 → not exhausted" "no" "$ex"
assert_eq "budget_remaining under an unbounded run is 0 (== 'no bound')" "0" "$(budget_remaining)"

budget_open 30
if budget_is_bounded; then bounded=yes; else bounded=no; fi
assert_eq "budget_open 30 → bounded" "yes" "$bounded"
if budget_exhausted; then ex=yes; else ex=no; fi
assert_eq "budget_open 30 → not yet exhausted" "no" "$ex"
rem=$(budget_remaining)
assert_le "budget_remaining is at most the budget" "30" "$rem"
[[ "$rem" -ge 28 ]] && pass "budget_remaining is ~the budget right after opening ($rem)" \
                    || fail "budget_remaining right after opening" ">= 28" "$rem"

echo ""
echo "== an exhausted budget reports itself, and never as 'unbounded' =="
# The one collision in the API: budget_remaining prints 0 both for "no bound"
# and for "nothing left". If budget_exhausted did not tell them apart, an
# expired budget would silently become an unbounded run — the exact outcome
# --budget exists to prevent.
budget_open 1
sleep 1.3
if budget_exhausted; then ex=yes; else ex=no; fi
assert_eq "a spent budget is exhausted" "yes" "$ex"
if budget_is_bounded; then bounded=yes; else bounded=no; fi
assert_eq "a spent budget is still BOUNDED (not silently unbounded)" "yes" "$bounded"
assert_eq "a spent budget has 0 remaining" "0" "$(budget_remaining)"

echo ""
echo "== budget_run 0 is a plain passthrough =="
budget_open 0
budget_run 0 true
assert_eq "budget_run 0 true → 0" "0" "$?"
budget_run 0 bash -c 'exit 7'
assert_eq "budget_run 0 passes the command's own status through" "7" "$?"
assert_eq "budget_run 0 sets no timeout flag" "0" "$BUDGET_LAST_TIMED_OUT"

echo ""
echo "== a fast command under a bound is unaffected =="
# The other half of the timeout evidence. A wrapper that reported TIMEOUT for
# everything would satisfy every negative case below and read as working.
start=$SECONDS
budget_run 30 bash -c 'exit 3'
rc=$?
elapsed=$((SECONDS - start))
assert_eq "a fast command keeps its own exit status under a bound" "3" "$rc"
assert_eq "a fast command sets no timeout flag" "0" "$BUDGET_LAST_TIMED_OUT"
assert_le "a fast command returns immediately, it does not wait out the bound" "3" "$elapsed"

start=$SECONDS
budget_run 30 true
rc=$?
elapsed=$((SECONDS - start))
assert_eq "a fast success under a bound → 0" "0" "$rc"
assert_le "and returns immediately" "3" "$elapsed"

echo ""
echo "== a command that exits 124 on its own is NOT reported as a timeout =="
# 124 is the status budget_run reports for a kill, so a caller classifying on
# the number alone would send the reader hunting for time nobody spent.
budget_run 30 bash -c "exit $BUDGET_TIMEOUT_RC"
assert_eq "a self-inflicted 124 is passed through" "$BUDGET_TIMEOUT_RC" "$?"
assert_eq "...and the timeout flag stays clear" "0" "$BUDGET_LAST_TIMED_OUT"

echo ""
echo "== a command that outlives its bound is killed and reported =="
start=$SECONDS
budget_run 1 sleep 60
rc=$?
elapsed=$((SECONDS - start))
assert_eq "an over-budget command → BUDGET_TIMEOUT_RC" "$BUDGET_TIMEOUT_RC" "$rc"
assert_eq "...and sets the timeout flag" "1" "$BUDGET_LAST_TIMED_OUT"
assert_le "...and does not wait out the command (1s bound, not 60s)" "8" "$elapsed"

echo ""
echo "== the whole process tree dies, not just the direct child =="
# `go test` forks a compiler and a test binary per package and npm forks node,
# so a bound that only kills the shell leaves the expensive part running after
# the hook has returned — a "bounded" run that keeps burning the machine.
#
# The fixture is a script that recurses to a fixed depth and whose LEAF `exec`s
# its sleep. Both halves of that are load-bearing (#1616).
#
# `exec`, because the first version of this case asked whether a surviving
# great-grandchild had written a marker file — and that great-grandchild was a
# SHELL running `sleep 30; echo … >marker`, so "the sleep was interrupted" and
# "the process survived" produced the same evidence. _budget_kill_tree signals
# depth-first, so the `sleep` is signalled BEFORE the shell waiting on it; if
# the walking shell is descheduled between those two kills, that shell reaps
# its dead child and runs the next command in the list. The marker appeared
# while `pgrep` found nothing — the writer was gone by the time the test
# looked — so the two assertions contradicted each other, on a required check,
# for a reason no reader could act on. Measured on the old fixture: 1 failure
# in 600 runs under load, and 100% of runs with that window widened by 400µs
# (the threshold is ~150µs, which is an ordinary preemption). After `exec`
# there IS no next command — the observed process is the `sleep` itself, so it
# either exists or it does not, and there is no mid-transition state to catch
# it in.
#
# A script rather than a nest of quoted `bash -c`s, because the pids are then
# reachable by a pattern unique to this run. The old survivor query was the bare
# literal `sleep 30; echo grandchild-survived`, and `pgrep -f` matches ANY
# process whose command line contains it — a concurrent copy of this suite, or
# simply the shell that invoked it. Measured: a `bash -c` whose argv merely
# quotes that string is reported as a survivor, so an agent or CI step that
# names the pattern (to clean up after it, say) fails the assertion 100% of the
# time for a reason that has nothing to do with the kill.
fixture=$(mktemp -t irrlicht-gate-budget-fixture)
leafpid=$(mktemp -t irrlicht-gate-budget-leafpid)
trap 'rm -f "$fixture" "$leafpid"' EXIT
cat >"$fixture" <<'FIXTURE'
#!/usr/bin/env bash
# $1 = file the leaf records its pid in; $2 = levels still to descend.
# Each level backgrounds the next and waits, so budget_run has a TREE to walk
# and not just a child: the leaf sits as deep below the pid it kills as a real
# gate's test binary sits below `go test`.
if [[ "${2:-0}" -gt 0 ]]; then
  bash "$0" "$1" "$(($2 - 1))" &
  wait
  exit
fi
echo $$ >"$1"
exec sleep 30
FIXTURE
# The bound is 2, not 1, and that is setup rather than slack for a race — the
# race is gone by construction above. $SECONDS counts whole seconds, so
# budget_run's first deadline check can fire almost immediately: `budget_run 1`
# gives its command anywhere from ~0.15s to ~1s (measured over phase-randomised
# runs), while the fixture needs ~12ms idle and ~35ms under heavy load to reach
# its leaf. At 2 the floor is ~1.2s, so the guard below reports "the tree was
# never built" only when something is really wrong. The old fixture had the
# same exposure and answered it by passing vacuously.
budget_run 2 bash "$fixture" "$leafpid" 2
assert_eq "the wrapper still reports a timeout" "1" "$BUDGET_LAST_TIMED_OUT"

leaf=$(cat "$leafpid" 2>/dev/null)
if [[ -z "$leaf" ]]; then
  # Loud, not silent: with no leaf pid there is nothing to look at, and "the
  # tree was never built" must not be reported as "the tree died".
  fail "the fixture reached its leaf before the bound expired" \
       "a recorded pid" "an empty pidfile — the tree-kill assertion never ran"
else
  # Poll to a generous deadline instead of sleeping a fixed 2s and looking
  # once. A fixed wait only has to be shorter than the machine is slow to turn
  # a correct kill into a red, and it makes the pass slower than it needs to be;
  # this returns the moment the tree is gone and says how long it waited when it
  # does not. The shells are matched by the fixture's (unique) path, the leaf by
  # the pid it recorded — after `exec` its command line is just `sleep 30`.
  start=$SECONDS
  alive=""
  while :; do
    alive="$(pgrep -f "$fixture" 2>/dev/null | tr '\n' ' ')"
    kill -0 "$leaf" 2>/dev/null && alive="$alive$leaf(the exec'd leaf)"
    [[ -z "$alive" ]] && break
    [[ $((SECONDS - start)) -ge 15 ]] && break
    sleep 0.1
  done
  elapsed=$((SECONDS - start))
  if [[ -z "$alive" ]]; then
    pass "no descendant of the killed command outlived the bound (gone after ${elapsed}s)"
  else
    fail "no descendant of the killed command may outlive the bound" \
         "every descendant gone within 15s" "still alive after ${elapsed}s: $alive"
  fi
  kill -0 "$leaf" 2>/dev/null && kill -9 "$leaf" 2>/dev/null  # never leak a 30s sleep
fi
pkill -f "$fixture" 2>/dev/null
rm -f "$fixture" "$leafpid"

echo ""
echo "== end to end: tools/preflight.sh names the gate that ran out of time =="
# One mutated COPY of preflight.sh, with the gofmt gate's command replaced by a
# sleep. gofmt is the first gate of the go group, so `--only go --budget 3`
# produces both verdicts in one run: gofmt TIMEOUT, then every gate behind it
# NOT RUN. No --changed, so this does not depend on the branch's diff.
probe="$ROOT/tools/.preflight-budget-probe-$$.sh"
# Replaces the tree-kill block's trap, so it re-names that block's files: both
# are already gone by here, but a reordering must not silently lose them.
trap 'rm -f "$probe" "$fixture" "$leafpid"' EXIT
python3 - "$ROOT/tools/preflight.sh" "$probe" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
s = open(src).read()
old = 'unformatted=$(gofmt -l core/ tools/)'
# #1390: assert the mutation applied. A harness whose substitution silently
# no-ops reports the UNMUTATED run as evidence, which is the failure this whole
# file is about, reproduced inside its own test.
assert s.count(old) == 1, f"MUTATION DID NOT APPLY: expected exactly one {old!r}, found {s.count(old)}"
open(dst, 'w').write(s.replace(old, 'unformatted=$(sleep 45)'))
PY
mutated=$?
assert_eq "the probe's mutation applied" "0" "$mutated"

if [[ "$mutated" -eq 0 ]]; then
  start=$SECONDS
  out=$(bash "$probe" --only go --budget 3 2>&1)
  rc=$?
  elapsed=$((SECONDS - start))
  assert_eq "an over-budget gate refuses the run" "1" "$rc"
  assert_contains "the timed-out gate is named" "gofmt" "$out"
  assert_contains "and its verdict is TIMEOUT" "TIMEOUT" "$out"
  # Read the verdict out of the summary row rather than grepping the whole
  # output for the word: the budget banner at the top of every bounded run
  # contains "TIMEOUT" as prose, so a whole-output grep passes for a gate that
  # was never graded at all.
  assert_eq "the summary row for gofmt reads TIMEOUT" "TIMEOUT" \
    "$(echo "$out" | awk '/^  gofmt {2,}/ {print $NF}')"
  assert_contains "the gates behind it are NOT RUN, not PASS" "NOT RUN" "$out"
  assert_contains "NOT RUN is distinguished from SKIP in words" "not a SKIP and not a pass" "$out"
  assert_contains "the closing verdict names the unfinished gates" "did not finish inside the 3s budget" "$out"
  assert_contains "and says how to run them" "--only go" "$out"
  assert_le "the bounded run really is bounded (3s budget, 45s gate)" "20" "$elapsed"

  # The vacuity half: the same mutated copy, on a group the mutation does not
  # touch, still passes. Without this, a probe that failed for any reason at
  # all would satisfy every assertion above.
  out=$(bash "$probe" --only skills --budget 120 2>&1)
  rc=$?
  assert_eq "an untouched, fast gate under a generous budget still passes" "0" "$rc"
  assert_eq "...and its summary row reads PASS, not TIMEOUT" "PASS" \
    "$(echo "$out" | awk '/^  skill-file lint {2,}/ {print $NF}')"
  case "$out" in
    *"did not finish inside"*) fail "a fast gate must not be listed as unfinished" "no unfinished list" "unfinished list present" ;;
    *) pass "a fast gate is not listed among the unfinished" ;;
  esac

  # And unbounded: --budget 0 must leave the run exactly as it was, so the
  # manual `tools/preflight.sh` full run is unchanged by any of this.
  out=$(bash "$probe" --only skills --budget 0 2>&1)
  rc=$?
  assert_eq "--budget 0 runs unbounded and still passes" "0" "$rc"
  case "$out" in
    *"budget: this run is bounded"*) fail "--budget 0 must not announce a bound" "no bound banner" "bound banner present" ;;
    *) pass "--budget 0 announces no bound" ;;
  esac
fi

echo ""
echo "== preflight.sh rejects a malformed --budget, as it does an unknown --only =="
out=$("$ROOT/tools/preflight.sh" --budget 10m 2>&1); rc=$?
assert_eq "tools/preflight.sh --budget 10m → 2" "2" "$rc"
assert_contains "...and says what it wanted" "whole number of seconds" "$out"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "gate-budget_test: ALL PASS"
else
  echo "gate-budget_test: $fails FAILED" >&2
  exit 1
fi
