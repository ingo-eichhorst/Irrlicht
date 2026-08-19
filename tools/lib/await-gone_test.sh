#!/usr/bin/env bash
# await-gone_test.sh — unit tests for lib/await-gone.sh. Plain bash, no
# framework, matching tools/lib/gate-budget_test.sh. Run directly, or via
# tools/preflight.sh's `tools` gate.
#
# Covers #1627. Two fixtures — gate-budget_test.sh's killed process tree and
# swift-suite_test.sh's killed grandchild — polled for a reap with two
# implementations of the same idiom, and only one of them bounded its deadline
# from above. This file grades the library both of them now call.
#
# ---------------------------------------------------------------------------
# What is a LOCK here and what is not
#
# The library is new, so almost nothing in it "passes by construction" in the
# sense AGENTS.md exempts: every assertion below is over behaviour this change
# ADDS, and each therefore owes a deliberate mutation seen red. Those mutations
# are in the PR body; the ones about the predicate contract are COMMITTED
# instead, as the deliberately-wrong predicates whose names end in
# `_predicate` — the shape `hookjson`'s `forgetfulReceiver` uses, so the
# evidence outlives the PR that added it. No count is stated, because nothing
# would keep one honest.
#
# The two that are genuinely locks — behaviour that predates this change and
# must not move — are named where they sit:
#
#   the RATIO REFUSAL at 15s against a 30s lifetime, which is #1627's
#   subject: the exact pair gate-budget_test.sh carried, and the pair
#   swift-suite_test.sh's own `(( reap_deadline * 10 <= gc_sleep ))` (#1619's
#   M4) would have refused had it been applied there.
#
#   GONE ON LOOK 1, which is #1619's measurement replayed as an assertion: a
#   subject already gone when the poll starts is reported at once rather than
#   after a fixed sleep.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=await-gone.sh
source "$DIR/await-gone.sh"

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to preflight's shell_lib_tests and to test.yml's loop, so the gate would
# go green having asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: await-gone_test — $1 not found" >&2; exit 1; }; }
need sleep

# Keep the suite in milliseconds rather than seconds. The interval is how long
# the poll waits between looks and changes nothing about what is asserted.
#
# shellcheck disable=SC2034  # read by the SOURCED library, not by this file:
# await-gone.sh defaults it at line 131 and sleeps on it at 283. shellcheck does
# not follow a source through a variable path, so it cannot see the consumer.
AWAIT_GONE_POLL_SECONDS=0.02

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]"; fails=$((fails + 1)); return 0; }
assert_eq() {
  if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "$2" "$3"; fi
}
# The refusal assertions name a FRAGMENT of the message, never just the status:
# every refusal here returns 2, so "it returned 2" is satisfied by the wrong
# refusal firing. Same rule contracttesting's negative self-tests follow.
assert_reason() {
  case "$AWAIT_GONE_REASON" in
    *"$2"*) pass "$1" ;;
    *) fail "$1" "a refusal naming: $2" "${AWAIT_GONE_REASON:-nothing was recorded}" ;;
  esac
}

echo ""
echo "== await_gone_bound: the wall from ABOVE (#1619's M4, #1627) =="

# THE LOCK. 15s against a 30s lifetime is exactly what gate-budget_test.sh
# carried, and exactly what swift-suite_test.sh's inline guard would have
# refused. A 2:1 ratio means "the fixture exited by itself" and "the fixture
# was reaped" produce the same reading.
await_gone_bound 15 30 "the leaf's own sleep" 2>/dev/null
assert_eq "refuses the 15s-against-30s pair #1627 found" "2" "$?"
assert_reason "...and the refusal names both numbers and the subject" "a 15s deadline is not 10x under the leaf's own sleep (30s)"

# The vacuity guard. A guard that refused everything would satisfy the case
# above and read as excellent coverage; these two are the pairs the callers
# actually use.
await_gone_bound 3 30 "the leaf's own sleep" 2>/dev/null
assert_eq "accepts 3s against 30s — gate-budget_test.sh's pair, exactly at the wall" "0" "$?"
await_gone_bound 15 300 "the grandchild's own lifetime" 2>/dev/null
assert_eq "accepts 15s against 300s — swift-suite_test.sh's pair" "0" "$?"

# One second past the wall. The boundary is where a guard is worth having: at
# 3/30 the caller sits ON it, so the next author who raises the deadline by one
# second is the case this must catch.
await_gone_bound 4 30 "the leaf's own sleep" 2>/dev/null
assert_eq "refuses 4s against 30s — one second past the wall" "2" "$?"

await_gone_bound "" 30 2>/dev/null
assert_eq "refuses an empty deadline" "2" "$?"
await_gone_bound 3s 30 2>/dev/null
assert_eq "refuses a non-numeric deadline" "2" "$?"
assert_reason "...naming what it could not read" "the deadline must be a whole number of seconds, got '3s'"
await_gone_bound 3 forever 2>/dev/null
assert_eq "refuses a non-numeric lifetime" "2" "$?"
await_gone_bound 0 30 2>/dev/null
assert_eq "refuses a zero deadline rather than reading it as 'look once'" "2" "$?"
assert_reason "...saying why zero is a different assertion" "'look exactly once' is a different assertion"

echo ""
echo "== await_gone re-checks the bound and fails CLOSED =="
# The caller states the bound where the two numbers are declared, so a wrong
# pair is reported before the fixture spends any time. That call is a
# convenience; this is the enforcement. A caller that skipped it, or that
# polled with numbers other than the ones it checked, must not get an
# unbounded poll — hookjson.DecodeConfined's shape.
already_gone() { AWAIT_GONE_LOOKED=1; AWAIT_GONE_ALIVE=""; }
await_gone 15 30 already_gone "the leaf's own sleep" 2>/dev/null
assert_eq "refuses a bad bound even when the subject IS gone" "2" "$?"
assert_reason "...with the bound's own refusal, not a poll result" "is not 10x under"

echo ""
echo "== a subject already gone is reported on LOOK 1 (#1619, #1627) =="
# The other lock. Measured 50/50 at look 1 / 0s for both callers, so the poll
# must report at once rather than after a fixed sleep — that is the whole
# difference between polling and #1586's sleeping fixture.
await_gone 3 30 already_gone "the leaf's own sleep"
rc=$?
assert_eq "returns 0 for a subject that is already gone" "0" "$rc"
assert_eq "...on the first look, with nothing in front of it" "1" "$AWAIT_GONE_LOOKS"
assert_eq "...and no time on the clock" "0" "$AWAIT_GONE_ELAPSED"

echo ""
echo "== a subject that outlives the deadline fails LOUDLY, with evidence =="
# The failure line has to self-identify: elapsed, look count and the last state
# observed. A bare pid is what swift-suite_test.sh's predecessor printed, and
# it cannot tell a live process from an unreaped zombie.
victim_cmd() { AWAIT_GONE_LOOKED=1; AWAIT_GONE_ALIVE=""; kill -0 "$victim" 2>/dev/null && AWAIT_GONE_ALIVE="pid $victim (state R, the fixture's own sleep)"; }
sleep 30 & victim=$!
await_gone 1 10 victim_cmd "the victim's own sleep"
rc=$?
assert_eq "returns 1 for a subject that is still there at the deadline" "1" "$rc"
[[ "$AWAIT_GONE_ELAPSED" -ge 1 ]] \
  && pass "...having actually waited to the deadline (${AWAIT_GONE_ELAPSED}s)" \
  || fail "...having actually waited to the deadline" ">= 1s" "${AWAIT_GONE_ELAPSED}s"
[[ "$AWAIT_GONE_LOOKS" -gt 1 ]] \
  && pass "...over more than one look ($AWAIT_GONE_LOOKS)" \
  || fail "...over more than one look" "> 1" "$AWAIT_GONE_LOOKS"
assert_eq "...carrying the last state observed, not a bare pid" "pid $victim (state R, the fixture's own sleep)" "$AWAIT_GONE_LAST"
kill -9 "$victim" 2>/dev/null
wait "$victim" 2>/dev/null

echo ""
echo "== 'could not look' is NOT 'it is gone' =="
# The committed mutation evidence for the rule #1619 recorded as a comment.
# Each of these three predicates is wrong in exactly ONE way, and every one of
# them reports NOTHING ALIVE — which is byte-identical to a reaped subject.
# A poll that read them at face value would return 0, i.e. would certify a kill
# it never observed.
blind_predicate() { AWAIT_GONE_LOOKED=0; AWAIT_GONE_ALIVE="pgrep is not on PATH"; }
await_gone 3 30 blind_predicate "the leaf's own sleep" 2>/dev/null
assert_eq "a predicate that could not look refuses (2), never passes (0)" "2" "$?"
assert_reason "...naming the predicate and why it was blind" "could not look on look 1: pgrep is not on PATH"

# The one above still has something to SAY. This one is the shape a real
# enumerator failure takes — `out=$(pgrep …)` that did not run enumerated
# nothing, so there is nothing to describe either — and it is the one that
# matters: an empty description is this poll's spelling of "the subject is
# gone", so without the LOOKED check it returns 0 and certifies a kill it never
# observed. Measured at the real call site: with `pgrep` replaced by a binary
# that is not on PATH, gate-budget_test.sh's PREDECESSOR poll printed
# `no descendant of the killed command outlived the bound (gone after 0s)` and
# `ALL PASS`.
blind_and_silent_predicate() { AWAIT_GONE_LOOKED=0; AWAIT_GONE_ALIVE=""; }
await_gone 3 30 blind_and_silent_predicate "the leaf's own sleep" 2>/dev/null
assert_eq "a predicate that could not look AND saw nothing still refuses" "2" "$?"
assert_reason "...rather than letting its silence read as 'gone'" "no reason given"

mute_predicate() { :; }
await_gone 3 30 mute_predicate "the leaf's own sleep" 2>/dev/null
assert_eq "a predicate that reports nothing at all refuses" "2" "$?"
assert_reason "...saying the two readings cannot be told apart" "no way to tell 'nothing is there' from 'nothing was checked'"

half_predicate() { AWAIT_GONE_LOOKED=1; }
await_gone 3 30 half_predicate "the leaf's own sleep" 2>/dev/null
assert_eq "a predicate that looked but described nothing refuses" "2" "$?"
assert_reason "...saying an unset description IS the word 'gone'" "never set AWAIT_GONE_ALIVE"

await_gone 3 30 no_such_predicate "the leaf's own sleep" 2>/dev/null
assert_eq "a predicate that is not a function at all refuses" "2" "$?"
assert_reason "...rather than polling to the deadline and reporting it gone" "is not a shell function"

echo ""
echo "== a broken \`ps\` DESCRIBES nothing; the builtin still decides =="
# swift-suite_test.sh's rule, replayed: `ps` may say WHAT state a surviving pid
# is in, but a missing or broken `ps` prints nothing, and nothing would
# otherwise read as reaped. The verdict comes from `kill -0`, a builtin that
# cannot fail to run; `ps` only fills in the description.
sleep 30 & victim=$!
broken_ps_predicate() {
  AWAIT_GONE_LOOKED=1
  AWAIT_GONE_ALIVE=""
  if kill -0 "$victim" 2>/dev/null; then
    local row
    row=$(irrlicht-no-such-ps -o stat= -p "$victim" 2>/dev/null)
    AWAIT_GONE_ALIVE="pid $victim${row:+ ($row)}"
  fi
}
await_gone 1 10 broken_ps_predicate "the victim's own sleep"
assert_eq "a live subject is still reported alive when the describer is gone" "1" "$?"
assert_eq "...described by what the builtin knows" "pid $victim" "$AWAIT_GONE_LAST"
kill -9 "$victim" 2>/dev/null
wait "$victim" 2>/dev/null

echo ""
echo "== the predicate runs in the CALLER's shell, not a subshell =="
# The seam is what keeps the rule above whole. Capturing a predicate's stdout
# would mean `$(…)` — a fork per look, on exactly the loaded machine the
# deadline exists for, whose failure mode is empty output, which is this
# poll's spelling of "gone". Reporting through variables costs a builtin
# predicate no fork at all. A subshell would lose every increment below, so
# this assertion is the seam rather than a description of it.
counted=0
# shellcheck disable=SC2034  # AWAIT_GONE_LOOKED / AWAIT_GONE_ALIVE are this
# predicate's whole OUTPUT — await-gone.sh reads both after every call (the
# `case` at 260-269 and the comparison at 271). Reporting through variables
# rather than stdout is the seam the comment above describes, so an "unused"
# verdict here is shellcheck failing to follow the source, not dead code.
counting_predicate() {
  counted=$(( counted + 1 ))
  AWAIT_GONE_LOOKED=1
  AWAIT_GONE_ALIVE=""
  [[ "$counted" -lt 4 ]] && AWAIT_GONE_ALIVE="still counting ($counted)"
  return 0
}
await_gone 3 30 counting_predicate "the counter's own lifetime"
assert_eq "the poll ends when the predicate says gone" "0" "$?"
assert_eq "the predicate's writes are visible to the caller" "4" "$counted"
assert_eq "...exactly once per look" "$AWAIT_GONE_LOOKS" "$counted"

echo ""
if [[ "$fails" -eq 0 ]]; then
  echo "await-gone_test: ALL PASS"
  exit 0
fi
echo "await-gone_test: $fails FAILED"
exit 1
