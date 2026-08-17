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
# shellcheck source=await-gone.sh
source "$DIR/await-gone.sh"

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
# assert_ge <label> <floor> <actual>
assert_ge() {
  [[ "$3" -ge "$2" ]] && pass "$1 ($3 >= $2)" || fail "$1" ">= $2" "$3"
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
# "the process survived" produced the same evidence. budget_run signals
# deepest-first, so the `sleep` is signalled BEFORE the shell waiting on it; if
# the signalling shell is descheduled between those two kills, that shell reaps
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
leafpid_ign=$(mktemp -t irrlicht-gate-budget-leafpid-ign)
trap 'rm -f "$fixture" "$leafpid" "$leafpid_ign"' EXIT
cat >"$fixture" <<'FIXTURE'
#!/usr/bin/env bash
# $1 = file the leaf records its pid in; $2 = levels still to descend;
# $3 = the leaf's own lifetime in seconds; $4 = the leaf's SIGTERM disposition,
# `term-dying` or `term-ignoring`.
# Each level backgrounds the next and waits, so budget_run has a TREE to walk
# and not just a child: the leaf sits as deep below the pid it kills as a real
# gate's test binary sits below `go test`.
#
# The lifetime is an ARGUMENT rather than a literal because the poll below is
# bounded against it (#1627), and a number the guard reads from one place and
# the fixture from another is a pair that can drift apart silently. `${3:?}`
# and not `$3`: without `set -u` an unset one expands to the empty string,
# `sleep ""` fails instantly, and a leaf that exits by itself the moment it is
# born makes the reap assertion pass having observed nothing. Refusing BEFORE
# the pid is recorded routes that to the loud "the tree was never built" branch.
leaf_sleep="${3:?the leaf lifetime must be passed as \$3}"
# The disposition is REQUIRED and its value is checked against a closed set,
# for the same reason and with the same routing (#1681). An absent or
# misspelled one defaulting to `term-dying` would silently turn the
# TERM-ignoring case below into a second copy of the TERM-dying one — a case
# that no longer reaches the defect it was written for, passing.
#
# No apostrophe in that message, deliberately: bash 3.2 treats a `'` inside a
# parameter expansion's word as a QUOTE even when the whole expansion is
# double-quoted, so "the leaf's disposition" here is a parse error for the
# WHOLE fixture. Measured — it is the same trap await-gone.sh records at
# AWAIT_GONE_DEFAULT_WHAT, and the assertion below is what names it.
leaf_term="${4:?the SIGTERM disposition of the leaf must be passed as \$4}"
case "$leaf_term" in
  term-dying | term-ignoring) ;;
  *) echo "fixture: unknown SIGTERM disposition '$leaf_term'" >&2; exit 64 ;;
esac
if [[ "${2:-0}" -gt 0 ]]; then
  bash "$0" "$1" "$(($2 - 1))" "$leaf_sleep" "$leaf_term" &
  wait
  exit
fi
# `trap ''` sets the disposition to IGNORE, and an ignored disposition is
# INHERITED ACROSS EXEC where a caught one is reset to the default — so the
# leaf keeps #1616's `exec` (no next command, no mid-transition state to catch
# it in) and is still the descendant SIGTERM does not reach. Measured on bash
# 3.2.57 / macOS 25.5: the exec'd `sleep` answers `kill -0` after a SIGTERM
# that would have ended it. Doing it with a HANDLER instead would reintroduce
# exactly the ambiguity #1616 removed.
[[ "$leaf_term" == "term-ignoring" ]] && trap '' TERM
echo $$ >"$1"
exec sleep "$leaf_sleep"
FIXTURE
# A fixture that will not PARSE fails every case below through the "the tree
# was never built" branch — loud, but pointing at the bound rather than at the
# heredoc, and the shell's own diagnostic lands on stderr several assertions
# away from the failure it caused. Asserted here so the cause is named where it
# happens. Not hypothetical: writing that `${4:?}` message with an apostrophe
# in it broke this file exactly once, and the run reported five failures none
# of which said "syntax error".
if bash -n "$fixture" 2>/dev/null; then
  pass "the process-tree fixture parses"
else
  fail "the process-tree fixture must parse before anything can be graded with it" \
       "a fixture bash accepts" "$(bash -n "$fixture" 2>&1 | tr '\n' ' ')"
fi
# The leaf's own lifetime and the deadline its reap is polled to below are
# declared as a PAIR, and await_gone_bound checks the relation between them
# rather than leaving it to arithmetic nobody runs (#1627). The pair used to be
# 30 and 15 — a 2:1 ratio, which still held the property with 15s to spare but
# held it by accident: trimming the leaf's sleep (it is there to be long, and a
# future author cutting test runtime would read 30s as waste) or raising the
# deadline (which is exactly what #1619 proposed doing to the sibling fixture)
# removes it with nothing objecting.
#
# 3s is the LARGEST value the wall admits against a 30s leaf, and it is chosen
# from that direction because the measurement gives nothing to choose from
# below: 50/50 runs — 25 idle at load ~4, 25 at load 27→53 on 10 cores — found
# this tree already gone on look 1 at 0s, budget_run having posted SIGKILL and
# `wait`ed before it returned. So the deadline is pure slack for a busy
# machine, and the leaf stays at 30 rather than growing to buy a longer one:
# the only cost a longer leaf carries is a bigger `sleep` to leak if this
# fixture is ever killed before its own cleanup runs.
leaf_sleep=30
reap_deadline=3
await_gone_bound "$reap_deadline" "$leaf_sleep" "the leaf's own sleep" \
  || fail "the reap deadline and the leaf's lifetime must stay an order of magnitude apart" \
          "await_gone_bound to accept ${reap_deadline}s against ${leaf_sleep}s" \
          "$AWAIT_GONE_REASON"

# ONE predicate for both tree-kill cases below, reading the two variables each
# case sets rather than taking arguments (await_gone calls its predicate with
# none). One rather than two because that is the whole finding behind #1627:
# two fixtures polling for the same thing, written two PRs apart, had drifted
# in four ways that were not stylistic.
#
# This is the half of await-gone's seam a single `kill -0` cannot cover: the
# intermediate shells are a SET, reachable only by the fixture's (unique) path,
# and `pgrep` is an external binary that can be missing or broken while `kill`
# is a builtin that cannot. So its status is read three ways rather than two —
# 0 matched, 1 matched nothing, anything else (2, 3, or 127 for a pgrep that is
# not there at all) means the enumeration did not happen, which is reported as
# "could not look" instead of joining the empty output that spells "gone". The
# leaf itself is matched by the pid it recorded, with the builtin: after `exec`
# its command line is just `sleep <n>`, and it is the process the cases are
# about.
tw_probe_fixture=""
tw_probe_leaf=""
# shellcheck disable=SC2034  # AWAIT_GONE_LOOKED / AWAIT_GONE_ALIVE are this
# predicate's output, read by await-gone.sh after every call — see
# tools/lib/await-gone.sh's header for the contract.
budget_tree_gone() {
  local shells rc
  shells=$(pgrep -f "$tw_probe_fixture" 2>/dev/null); rc=$?
  if [[ "$rc" -gt 1 ]]; then
    AWAIT_GONE_LOOKED=0
    AWAIT_GONE_ALIVE="pgrep -f exited $rc, so the fixture's shells were never enumerated"
    return 0
  fi
  AWAIT_GONE_LOOKED=1
  AWAIT_GONE_ALIVE="${shells//$'\n'/ }"
  kill -0 "$tw_probe_leaf" 2>/dev/null && AWAIT_GONE_ALIVE="$AWAIT_GONE_ALIVE$tw_probe_leaf(the exec'd leaf)"
  return 0
}
# The bound is 2, not 1, and that is setup rather than slack for a race — the
# race is gone by construction above. $SECONDS counts whole seconds, so
# budget_run's first deadline check can fire almost immediately: `budget_run 1`
# gives its command anywhere from ~0.15s to ~1s (measured over phase-randomised
# runs), while the fixture needs ~12ms idle and ~35ms under heavy load to reach
# its leaf. At 2 the floor is ~1.2s, so the guard below reports "the tree was
# never built" only when something is really wrong. The old fixture had the
# same exposure and answered it by passing vacuously.
#
# The grace period is raised from the suite-wide 1s for the two tree cases,
# and the number is what makes the two elapsed assertions below separable
# rather than a preference (#1681). budget_run's own deadline check is on
# `$SECONDS`, so a `budget_run 2` detects its bound anywhere in (1.0s, 2.05s],
# and the grace loop spends (grace-1, grace] on top: at grace=4 a run that
# spends the grace lands in (4.0s, 6.1s] and one that does not lands in
# (1.0s, 2.1s], which a whole-second clock can tell apart with a margin on
# each side. At the suite-wide 1s the two populations are adjacent and the
# assertions would be measuring scheduler noise.
tree_grace=4
# shellcheck disable=SC2034  # read by gate-budget.sh, sourced above
BUDGET_TERM_GRACE_SECONDS="$tree_grace"

start=$SECONDS
budget_run 2 bash "$fixture" "$leafpid" 2 "$leaf_sleep" term-dying
dying_elapsed=$((SECONDS - start))
assert_eq "the wrapper still reports a timeout" "1" "$BUDGET_LAST_TIMED_OUT"
# The other direction of the grace loop's condition, and the vacuity guard for
# the assertion in the TERM-ignoring case below: a loop that had simply dropped
# its condition — spinning out the full grace on every timeout — would satisfy
# that one just as well, and would make every gate's TIMEOUT cost the grace it
# does not need. Here the whole tree is gone milliseconds after the SIGTERM, so
# the grace must not be spent at all.
assert_le "a tree that dies on SIGTERM does not spend the grace period" "3" "$dying_elapsed"

leaf=$(cat "$leafpid" 2>/dev/null)
if [[ -z "$leaf" ]]; then
  # Loud, not silent: with no leaf pid there is nothing to look at, and "the
  # tree was never built" must not be reported as "the tree died".
  fail "the fixture reached its leaf before the bound expired" \
       "a recorded pid" "an empty pidfile — the tree-kill assertion never ran"
else
  # Polled to a deadline rather than slept, through the shared poll in
  # tools/lib/await-gone.sh (#1627) — which is also where the deadline/lifetime
  # wall above and the rule this predicate follows are written down once.
  tw_probe_fixture="$fixture"
  tw_probe_leaf="$leaf"
  await_gone "$reap_deadline" "$leaf_sleep" budget_tree_gone "the leaf's own sleep"
  case $? in
    0) pass "no descendant of the killed command outlived the bound (gone on look $AWAIT_GONE_LOOKS, after ${AWAIT_GONE_ELAPSED}s)" ;;
    1) fail "no descendant of the killed command may outlive the bound" \
            "every descendant gone within ${reap_deadline}s" \
            "still alive after ${AWAIT_GONE_ELAPSED}s / $AWAIT_GONE_LOOKS looks: $AWAIT_GONE_LAST" ;;
    *) fail "the reap had to be observable at all" \
            "a poll that could look" "$AWAIT_GONE_REASON" ;;
  esac
  kill -0 "$leaf" 2>/dev/null && kill -9 "$leaf" 2>/dev/null  # never leak the leaf's sleep
fi
pkill -f "$fixture" 2>/dev/null
rm -f "$leafpid"

echo ""
echo "== the SIGKILL pass reaches a descendant that SURVIVED the SIGTERM (#1681) =="
# The case above cannot see this one: its leaf is a plain `exec sleep`, which
# dies on SIGTERM, so budget_run's second pass only ever signals corpses and
# nothing depends on it reaching anybody. A descendant that IGNORES SIGTERM is
# what makes the SIGKILL pass load-bearing — and it is not a contrived shape,
# it is the handler-carrying processes (the ones that clean up on TERM, i.e.
# exactly the slow ones) that a bound has to reach.
#
# What #1681 measured on the unfixed library: budget_run reported a correct
# TIMEOUT and returned, and the leaf was still there with 40+ seconds of its
# sleep to go. The SIGKILL pass re-walked `pgrep -P` from a pid that SIGTERM
# had already killed, so it enumerated nobody — the descendants had been
# reparented to launchd — and signalled a corpse.

# The vacuity guard, and it is not optional: if the leaf turned out to die on
# SIGTERM after all — a fixture edit dropping the trap, an `exec` that reset
# the disposition, a future macOS that does not inherit it — the case below
# would pass while asserting nothing beyond what the case above already
# asserts, which is this repo's most-repeated failure shape. So the property
# the case rests on is OBSERVED here, on every run, rather than cited from a
# measurement in a merged PR body.
#
# Depth 0, so the leaf is a direct child of this shell and its own SIGTERM is
# the only one in play. `await_gone` is expected to return 1 — the subject
# survived to the deadline — which is the loudest available spelling of "it is
# still there": a positive observation bounded from both ends, not a sleep.
: >"$leafpid_ign"
bash "$fixture" "$leafpid_ign" 0 "$leaf_sleep" term-ignoring &
ign_child=$!
ign_wait=$((SECONDS + 5))
while [[ ! -s "$leafpid_ign" && "$SECONDS" -lt "$ign_wait" ]]; do sleep 0.05; done
ign_leaf=$(cat "$leafpid_ign" 2>/dev/null)
if [[ -z "$ign_leaf" ]]; then
  fail "the TERM-ignoring leaf had to record its pid before it could be graded" \
       "a recorded pid within 5s" "an empty pidfile — the vacuity guard never ran"
else
  kill -TERM "$ign_leaf" 2>/dev/null
  tw_probe_fixture="$fixture"
  tw_probe_leaf="$ign_leaf"
  # 1s against a 30s leaf: signal delivery is measured in microseconds, so this
  # is six orders of magnitude of slack, and the pair still clears
  # await_gone_bound's 10:1 wall.
  await_gone 1 "$leaf_sleep" budget_tree_gone "the leaf's own sleep"
  case $? in
    1) pass "the fixture's TERM-ignoring leaf really does survive a SIGTERM (${AWAIT_GONE_ELAPSED}s / $AWAIT_GONE_LOOKS looks: $AWAIT_GONE_LAST)" ;;
    0) fail "the case below is vacuous unless its leaf survives SIGTERM" \
            "a leaf still alive 1s after SIGTERM" \
            "it was gone on look $AWAIT_GONE_LOOKS — the trap did not take, so the SIGKILL pass is not what is being graded" ;;
    *) fail "the leaf's SIGTERM disposition had to be observable at all" \
            "a poll that could look" "$AWAIT_GONE_REASON" ;;
  esac
fi
kill -9 "$ign_leaf" 2>/dev/null
wait "$ign_child" 2>/dev/null
: >"$leafpid_ign"

# And now the case itself: the same tree, under the same bound, with the leaf
# SIGTERM cannot reach.
start=$SECONDS
budget_run 2 bash "$fixture" "$leafpid_ign" 2 "$leaf_sleep" term-ignoring
ign_elapsed=$((SECONDS - start))
assert_eq "the wrapper still reports a timeout (TERM-ignoring leaf)" "1" "$BUDGET_LAST_TIMED_OUT"
# The grace loop's SUBJECT, which the reap assertion below cannot see: with the
# victims collected before the SIGTERM, a loop still gated on the wrapper alone
# posts the SIGKILL immediately and the leaf is just as dead — sooner. What is
# lost is the grace itself, for every gate whose children clean up on SIGTERM
# rather than ignoring it, which is the only reason the grace period exists.
# The wrapper is the wrong subject because it is an ordinary `bash` that dies
# on SIGTERM in milliseconds whatever it is waiting for (#1616's corollary:
# observe the SUBJECT, not a side effect it produces on its way out).
assert_ge "a descendant still alive holds the grace period open" "$tree_grace" "$ign_elapsed"

leaf_ign=$(cat "$leafpid_ign" 2>/dev/null)
if [[ -z "$leaf_ign" ]]; then
  fail "the fixture reached its TERM-ignoring leaf before the bound expired" \
       "a recorded pid" "an empty pidfile — the SIGKILL assertion never ran"
else
  tw_probe_fixture="$fixture"
  tw_probe_leaf="$leaf_ign"
  await_gone "$reap_deadline" "$leaf_sleep" budget_tree_gone "the leaf's own sleep"
  case $? in
    0) pass "a descendant that ignored SIGTERM is still gone when the bound returns (gone on look $AWAIT_GONE_LOOKS, after ${AWAIT_GONE_ELAPSED}s)" ;;
    1) fail "the SIGKILL pass must reach a descendant that survived the SIGTERM" \
            "every descendant gone within ${reap_deadline}s" \
            "still alive after ${AWAIT_GONE_ELAPSED}s / $AWAIT_GONE_LOOKS looks: $AWAIT_GONE_LAST" ;;
    *) fail "the reap had to be observable at all" \
            "a poll that could look" "$AWAIT_GONE_REASON" ;;
  esac
  kill -0 "$leaf_ign" 2>/dev/null && kill -9 "$leaf_ign" 2>/dev/null  # never leak the leaf's sleep
fi
pkill -f "$fixture" 2>/dev/null
rm -f "$fixture" "$leafpid_ign"
# Back to the suite-wide grace: everything after this point is about statuses
# and shell options, not about how long a kill takes.
# shellcheck disable=SC2034  # read by gate-budget.sh, sourced above
BUDGET_TERM_GRACE_SECONDS=1

echo ""
echo "== budget_run under the CALLER's \`-e\` (#1635) =="
# gate-budget.sh is SOURCED (tools/preflight.sh), so budget_run runs with
# whatever shell options its caller happens to have. Unlike #1633's sibling
# defect this broke the ORDINARY path as well as the timeout one, and what it
# produced there is a false accusation: the backgrounded child inherits errexit
# and dies on a command that exits non-zero BEFORE writing the status file that
# is its "it finished" signal (gate-budget.sh's own comment above it), so
# budget_run polls to the full deadline and reports a TIMEOUT for a command
# that exited in milliseconds. Measured on the unfixed library: `exit 3` came
# back 1 after 19.55s, with BUDGET_LAST_TIMED_OUT never set — every failing
# gate reported as a timeout it was not, and every gate behind it as NOT RUN.
#
# Everything below is driven under `bash --noprofile --norc -e -o pipefail`,
# which is GitHub's own `shell: bash` invocation, in TWO different calling
# shapes because bash's errexit-context rule makes each blind to the other's
# defect:
#
#   the STATUS is read in a BARE statement position, the shape this library's
#            header documents. Called from a `||` or an `if`, bash ignores -e
#            for the whole function body — and, measured, for the backgrounded
#            child too — so the hazard cannot fire there and every assertion
#            below would pass against the unfixed library.
#   the LEAK is read where a line after the call still runs, which under -e a
#            bare non-zero return does not: the caller aborts on it. A `||`
#            position still EXECUTES any `set` the body performs, so that is
#            exactly where a library that turned -e off and forgot becomes
#            visible.
#
# BUDGET_LAST_TIMED_OUT is read in the bare shape anyway, through an EXIT trap:
# the flag is the signal preflight.sh classifies on, and reading it in a `||`
# probe would read it in the one shape where it was never wrong.
e_lib="$DIR/gate-budget.sh"
e_marker=$(mktemp -t gate-budget-e-marker)
e_flag=$(mktemp -t gate-budget-e-flag)
e_before=$(mktemp -t gate-budget-e-opts-before)
e_after=$(mktemp -t gate-budget-e-opts-after)
export E_LIB="$e_lib" E_MARKER="$e_marker" E_FLAG="$e_flag" \
       E_OPTS_BEFORE="$e_before" E_OPTS_AFTER="$e_after"

# --- the ordinary path: a command that fails INSTANTLY ----------------------
rm -f "$e_marker" "$e_flag"
start=$SECONDS
e_out=$(bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<'PROBE'
set -uo pipefail
. "$E_LIB"
BUDGET_POLL_SECONDS=0.05
BUDGET_TERM_GRACE_SECONDS=1
# Runs even when errexit aborts the shell, which is the only way to read the
# flag from the bare shape a correct 3 also aborts on.
trap 'echo "$BUDGET_LAST_TIMED_OUT" > "$E_FLAG"' EXIT
budget_run 20 bash -c ': > "$E_MARKER"; exit 3'
PROBE
)
e_rc=$?
elapsed=$((SECONDS - start))

# A verification that could not run must not read as one that found nothing:
# an inner shell that died before ever launching the command (a bad path, a
# source that failed) also exits 1, which is byte-identical to the defect.
if [[ -e "$e_marker" ]]; then
  pass "the -e probe actually ran its command (the fixture left its marker)"
else
  fail "the -e probe never launched its command, so the assertions below measured something else" \
       "a marker at $e_marker" "no marker; it printed: $e_out"
fi
assert_eq "a command that exits 3 returns 3 under the caller's -e" "3" "$e_rc"
assert_le "...and does not burn the budget waiting for a command that already exited" "8" "$elapsed"
assert_eq "...and does not set the timeout flag" "0" "$(cat "$e_flag" 2>/dev/null)"

# --- the timeout path -------------------------------------------------------
rm -f "$e_marker" "$e_flag"
e_out=$(bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<'PROBE'
set -uo pipefail
. "$E_LIB"
BUDGET_POLL_SECONDS=0.05
BUDGET_TERM_GRACE_SECONDS=1
trap 'echo "$BUDGET_LAST_TIMED_OUT" > "$E_FLAG"' EXIT
budget_run 2 bash -c ': > "$E_MARKER"; sleep 60'
PROBE
)
e_rc=$?
if [[ -e "$e_marker" ]]; then
  pass "the -e timeout probe actually ran its command (the fixture left its marker)"
else
  fail "the -e timeout probe never launched its command" \
       "a marker at $e_marker" "no marker; it printed: $e_out"
fi
assert_eq "an over-budget command returns BUDGET_TIMEOUT_RC under the caller's -e" \
          "$BUDGET_TIMEOUT_RC" "$e_rc"
assert_eq "...and DOES set the timeout flag" "1" "$(cat "$e_flag" 2>/dev/null)"

# --- and none of it is paid for by leaking the caller's options -------------
# `set +o` REDIRECTED TO A FILE, never `$(set +o)`: bash 3.2 — /bin/bash on
# macOS — reports errexit and nounset as OFF inside a command substitution no
# matter what the parent has (measured on 3.2.57), so a probe built the obvious
# way is byte-identical before and after and can never see a leak. `set` is a
# builtin, so redirecting it does not fork. Both paths are driven in the one
# probe, because a `set +e` added to either would leak from either.
rm -f "$e_before" "$e_after"
leak_out=$(bash --noprofile --norc -e -o pipefail /dev/stdin 2>&1 <<'PROBE'
set -uo pipefail
. "$E_LIB"
BUDGET_POLL_SECONDS=0.05
BUDGET_TERM_GRACE_SECONDS=1
set +o > "$E_OPTS_BEFORE"
ordinary=0; budget_run 20 bash -c 'exit 3' || ordinary=$?
timedout=0; budget_run 2 bash -c 'sleep 60' || timedout=$?
set +o > "$E_OPTS_AFTER"
echo "leak probe: ordinary=$ordinary timeout=$timedout"
PROBE
)
if [[ ! -s "$e_before" || ! -s "$e_after" ]]; then
  fail "the option-leak probe recorded no before/after option dump — it could not run" \
       "two non-empty dumps" "it printed: $leak_out"
elif diff "$e_before" "$e_after" >/dev/null 2>&1; then
  pass "the caller's shell options are unchanged across the call"
else
  fail "budget_run LEAKED a shell option change back to its caller" "no diff" \
       "$(diff "$e_before" "$e_after" | tr '\n' ' ')"
fi
rm -f "$e_marker" "$e_flag" "$e_before" "$e_after"
unset E_LIB E_MARKER E_FLAG E_OPTS_BEFORE E_OPTS_AFTER

echo ""
echo "== end to end: tools/preflight.sh names the gate that ran out of time =="
# One mutated COPY of preflight.sh, with the gofmt gate's command replaced by a
# sleep. gofmt is the first gate of the go group, so `--only go --budget 3`
# produces both verdicts in one run: gofmt TIMEOUT, then every gate behind it
# NOT RUN. No --changed, so this does not depend on the branch's diff.
probe="$ROOT/tools/.preflight-budget-probe-$$.sh"
# Replaces the tree-kill block's trap, so it re-names that block's files: both
# are already gone by here, but a reordering must not silently lose them.
trap 'rm -f "$probe" "$fixture" "$leafpid" "$leafpid_ign"' EXIT
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
