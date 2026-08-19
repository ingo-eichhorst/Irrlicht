#!/usr/bin/env bash
# await-gone.sh — poll until a process, or a set of them, is gone. Bounded from
# BOTH ends, and loud when it could not look at all (#1627).
#
# This file is sourced, not executed:
#
#   . tools/lib/await-gone.sh
#
#   leaf_gone() {                       # the predicate — see "The predicate"
#     AWAIT_GONE_LOOKED=1               # I was able to look
#     AWAIT_GONE_ALIVE=""               # ...and nothing is there
#     kill -0 "$pid" 2>/dev/null && AWAIT_GONE_ALIVE="pid $pid"
#   }
#
#   deadline=3 lifetime=30
#   await_gone_bound "$deadline" "$lifetime" "the leaf's own sleep" || fail ...
#   await_gone "$deadline" "$lifetime" leaf_gone "the leaf's own sleep"
#   case $? in
#     0) pass "gone on look $AWAIT_GONE_LOOKS, after ${AWAIT_GONE_ELAPSED}s" ;;
#     1) fail "still there after ${AWAIT_GONE_ELAPSED}s / $AWAIT_GONE_LOOKS looks; last seen: $AWAIT_GONE_LAST" ;;
#     2) fail "$AWAIT_GONE_REASON" ;;
#   esac
#
# Shell options, both directions, because every other library here now says so
# (#1633, #1635):
#
#   this file  requires nothing of the caller's options and changes none of
#              them. Both functions RETURN their verdict — 0, 1 or 2 — whether
#              or not `-e` is set. The caller's predicate is invoked as the
#              left operand of `||`, so errexit is ignored for the whole of its
#              body: a predicate built on `kill -0`, whose non-zero return is
#              its ORDINARY answer, cannot abort the caller from in here.
#   the caller must keep `$?` reachable if it wants to tell the three apart.
#              Under `-e` a bare non-zero return aborts the caller on that
#              line — write `|| rc=$?`, or read `$?` in a `case` as above
#              (#1629).
#
# ---------------------------------------------------------------------------
# Why this exists
#
# Two fixtures polled for a process tree to be reaped, written two PRs apart,
# and they had drifted in four ways that are not stylistic — `$SECONDS` vs
# `date +%s`, a SET matched by pattern vs ONE recorded pid, only one of them
# describing the last-observed state, and only one of them checking the
# deadline against the fixture's own lifetime. The last of those is the defect
# #1627 was filed about; the rest is why fixing it in place would have left a
# third fixture inheriting nothing.
#
# Three rules live here rather than in a comment in one of them.
#
# 1. POLLED, NOT SLEPT (#1586, #1619). A fixture that waits by sleeping a fixed
#    interval and then looks once has not observed what it waits for: the sleep
#    only has to be shorter than the machine is slow to turn a correct kill into
#    a red, and it makes the passing case slower than it needs to be. The first
#    look here happens with nothing in front of it, so a tree already reaped
#    when the killer returned — which is what a correct implementation produces,
#    since it posts SIGKILL and `wait`s before returning — is reported at once.
#    Measured for both current callers, and neither has a lower bound worth
#    speaking of: 50/50 gone on look 1 at 0s for gate-budget's tree (25 idle at
#    load ~4, 25 at load 27→53 on 10 cores, #1627) and the same 50/50 for
#    swift-suite's grandchild (#1619). The deadline is therefore slack for a
#    machine too busy to finish a teardown, never a latency being fitted.
#
# 2. THE WALL FROM ABOVE — await_gone_bound (#1619's M4, #1627). Precisely
#    because there is no latency to fit, nothing pulls the deadline DOWN, and a
#    deadline anywhere near the subject's own lifetime stops asserting
#    anything: "the fixture exited by itself" and "the fixture was reaped"
#    produce the same reading, and the case passes vacuously. So the deadline
#    and the subject's lifetime are declared as a PAIR and the relation between
#    them is checked rather than assumed. AWAIT_GONE_RATIO is the order of
#    magnitude that separates the two readings; it is a constant, not a knob.
#
# 3. "COULD NOT LOOK" IS NOT "IT IS GONE". This repo's central bug family
#    (#1485/#1492/#1513/#1524/#1533/#1537, and AGENTS.md's "a verification
#    mechanism must fail loudly when it cannot run") lands here in its purest
#    form: a predicate that fails to RUN reports nothing, and nothing is
#    byte-identical to a subject that is gone — so the poll returns success the
#    instant it goes blind. #1619 answered that by choosing `kill -0`, a shell
#    BUILTIN, as the predicate, so there is no PATH lookup, no exec and no
#    external binary that can be missing. That is still the rule and a
#    predicate should be built from builtins wherever it can be.
#
#    But it cannot always be: gate-budget's fixture polls a SET of shells that
#    only `pgrep -f <pattern>` can enumerate, and `pgrep` is an external binary
#    like any other. So the rule is backed by a mechanism as well as stated: a
#    predicate reports whether it was ABLE to look (AWAIT_GONE_LOOKED)
#    separately from what it SAW (AWAIT_GONE_ALIVE), and a predicate that could
#    not look is a loud refusal (2) rather than a pass (0). A predicate that
#    returns without setting either is a refusal too — the alternative is that
#    the previous look's values survive into this one, which is the same
#    failure wearing a different hat.
#
# ---------------------------------------------------------------------------
# The predicate, and why it reports through VARIABLES
#
# A predicate is a shell FUNCTION taking no arguments. It sets:
#
#   AWAIT_GONE_LOOKED  1 if it was able to look at all, 0 if it was not.
#   AWAIT_GONE_ALIVE   when LOOKED=1: a description of everything still there,
#                      or the empty string when the subject is gone. When
#                      LOOKED=0: why it could not look.
#
# BOTH are cleared to a sentinel before every call and BOTH must be set. A
# predicate that reports one and not the other is refused rather than read at
# its default, because every default here is one of the two answers: leaving
# LOOKED unset would inherit the previous look's "I looked", and leaving ALIVE
# unset at the empty string is literally the word "gone".
#
# Its return status is ignored on purpose — `kill -0` and `pgrep` both answer
# "nothing there" with a non-zero status, so a status-carried verdict would be
# the same conflation this file exists to remove.
#
# Reporting through variables rather than stdout is what keeps rule 3 whole.
# Capturing stdout means `desc=$(predicate)`, which forks a subshell per look;
# a builtin predicate would then depend on a fork succeeding, on exactly the
# loaded machine the deadline exists for, and the fork's failure mode is
# precisely the empty-output-reads-as-gone shape above. Called plainly, a
# predicate made of builtins costs no fork at all and its assignments are
# visible here. tools/lib/await-gone_test.sh asserts that seam directly: a
# predicate incrementing a counter must have incremented it exactly
# AWAIT_GONE_LOOKS times when await_gone returns, which a subshell would lose.
#
# `ps` — the only way to say WHAT state a surviving pid is in — may DESCRIBE a
# subject inside a predicate but must never decide the verdict: a missing or
# broken `ps` prints nothing, and nothing would otherwise read as reaped. Set
# AWAIT_GONE_LOOKED from the builtin and use `ps` only to fill in
# AWAIT_GONE_ALIVE.

# How long to wait between looks. A knob only so this library's own tests can
# run in milliseconds; both real callers use the default.
: "${AWAIT_GONE_POLL_SECONDS:=0.1}"

# The order of magnitude await_gone_bound requires between the poll deadline
# and the subject's own lifetime. NOT a knob: lowering it is exactly the edit
# the guard exists to refuse, and it would be invisible in a diff that also
# raised a deadline.
AWAIT_GONE_RATIO=10

# Set by await_gone. Declared here so a caller reading them under `set -u`
# after a refusal that returned before the loop still gets a defined value.
AWAIT_GONE_LOOKS=0
AWAIT_GONE_ELAPSED=0
AWAIT_GONE_LAST=""
AWAIT_GONE_REASON=""

# Set by the predicate, read by await_gone. See "The predicate" above.
AWAIT_GONE_LOOKED=""
AWAIT_GONE_ALIVE=""

# What both of the above are cleared to before each look. Any value that is not
# a legal answer would do; this one is spelled so that it cannot be mistaken
# for a description of a real process if it ever reaches a failure line.
AWAIT_GONE_UNREPORTED='<the predicate did not report>'

# What a caller that names no subject gets in the refusal text. A variable and
# not an inline `${3:-…}` default, because bash treats a `'` inside a parameter
# expansion's word as a QUOTE even when the whole expansion is double-quoted —
# measured on 3.2.57: `x="${1:-the subject's own lifetime}"` is a parse error
# ("unexpected EOF while looking for matching `''"), where the same text
# reached through a variable is fine.
AWAIT_GONE_DEFAULT_WHAT="the subject's own lifetime"

# _await_gone_refuse <message> — record and print a refusal. One place, so a
# refusal always reaches both the caller's AWAIT_GONE_REASON and stderr; a
# refusal that only set the variable would be silent in the log of a caller
# that forgot to print it.
_await_gone_refuse() {
  AWAIT_GONE_REASON="await-gone: refusing — $1"
  printf '%s\n' "$AWAIT_GONE_REASON" >&2
  return 2
}

# await_gone_bound <deadline> <lifetime> [what] — the wall from above (rule 2).
# 0 when the deadline is an order of magnitude under the subject's own
# lifetime, 2 with a named reason otherwise.
#
# Callers state it at the point where the two numbers are DECLARED, so a wrong
# pair is reported before the fixture spends any time; await_gone re-checks it
# anyway and fails closed, so skipping the call cannot buy an unbounded poll.
await_gone_bound() {
  local deadline="${1:-}" lifetime="${2:-}" what="${3:-$AWAIT_GONE_DEFAULT_WHAT}"
  AWAIT_GONE_REASON=""
  case "$deadline" in
    '' | *[!0-9]*)
      _await_gone_refuse "the deadline must be a whole number of seconds, got '$deadline'"
      return 2 ;;
  esac
  case "$lifetime" in
    '' | *[!0-9]*)
      _await_gone_refuse "$what must be a whole number of seconds, got '$lifetime'"
      return 2 ;;
  esac
  # A zero deadline is refused rather than accepted as "look once": the ratio
  # below is vacuously satisfied by it, so it would slip through the one check
  # that gives this poll its meaning while being a different assertion
  # entirely.
  if [ "$deadline" -lt 1 ]; then
    _await_gone_refuse "the deadline must be at least 1s; 'look exactly once' is a different assertion and the ratio check below cannot bound it"
    return 2
  fi
  if [ "$lifetime" -lt 1 ]; then
    _await_gone_refuse "$what must be at least 1s, got ${lifetime}s — there is nothing for the deadline to be under"
    return 2
  fi
  if [ "$(( deadline * AWAIT_GONE_RATIO ))" -le "$lifetime" ]; then
    return 0
  fi
  _await_gone_refuse "a ${deadline}s deadline is not ${AWAIT_GONE_RATIO}x under $what (${lifetime}s) — at that ratio a subject that exited BY ITSELF reads exactly like one that was reaped, and the poll stops asserting anything"
  return 2
}

# await_gone <deadline> <lifetime> <predicate-fn> [what] — poll until the
# predicate says the subject is gone.
#
#   0  gone. AWAIT_GONE_LOOKS / AWAIT_GONE_ELAPSED say how quickly.
#   1  still there when the deadline expired. AWAIT_GONE_LAST carries the last
#      state observed, so the caller's failure line can self-identify (a
#      zombie, say) instead of printing a bare pid.
#   2  the poll could not be judged at all: a bad bound, no such predicate, or
#      a predicate that could not look. Distinct from 1 because "it survived"
#      and "nothing was checked" are different answers, and AWAIT_GONE_REASON
#      names which.
await_gone() {
  local deadline="${1:-}" lifetime="${2:-}" pred="${3:-}" what="${4:-$AWAIT_GONE_DEFAULT_WHAT}"

  AWAIT_GONE_LOOKS=0
  AWAIT_GONE_ELAPSED=0
  AWAIT_GONE_LAST=""
  AWAIT_GONE_REASON=""

  # Re-checked here rather than trusted to the caller, and it fails CLOSED:
  # the bound is the only thing that makes this poll assert anything, so a
  # caller that skipped await_gone_bound, or called it with numbers other than
  # the ones it then polls with, must not get an unbounded poll. Same shape as
  # hookjson.DecodeConfined re-checking its declared permission set.
  await_gone_bound "$deadline" "$lifetime" "$what" || return 2

  if [ -z "$pred" ] || ! declare -F "$pred" >/dev/null 2>&1; then
    _await_gone_refuse "'$pred' is not a shell function, so there is no predicate to look with — a poll with nothing to ask would run to the deadline and report the subject gone"
    return 2
  fi

  local start=$SECONDS
  while :; do
    AWAIT_GONE_LOOKS=$(( AWAIT_GONE_LOOKS + 1 ))
    # Cleared before every call so a predicate that returns without reporting
    # is caught here instead of silently re-using the previous look's answer.
    AWAIT_GONE_LOOKED="$AWAIT_GONE_UNREPORTED"
    AWAIT_GONE_ALIVE="$AWAIT_GONE_UNREPORTED"
    # `|| :` and not a bare call: a predicate's ordinary answer is a non-zero
    # status (`kill -0` on a dead pid, `pgrep` matching nothing), and a
    # compound left operand of `||` runs with errexit ignored throughout — so
    # a caller with `-e` set neither dies inside the predicate nor needs to
    # know this. The status is discarded by design; the verdict is the two
    # variables above.
    "$pred" || :
    AWAIT_GONE_ELAPSED=$(( SECONDS - start ))

    case "$AWAIT_GONE_LOOKED" in
      1) ;;
      0)
        local why="$AWAIT_GONE_ALIVE"
        [ "$why" = "$AWAIT_GONE_UNREPORTED" ] && why="no reason given"
        _await_gone_refuse "the predicate '$pred' could not look on look $AWAIT_GONE_LOOKS: ${why:-no reason given}. Being unable to look must not be reported as the subject being gone"
        return 2 ;;
      *)
        _await_gone_refuse "the predicate '$pred' returned without setting AWAIT_GONE_LOOKED on look $AWAIT_GONE_LOOKS, so there is no way to tell 'nothing is there' from 'nothing was checked'"
        return 2 ;;
    esac

    if [ "$AWAIT_GONE_ALIVE" = "$AWAIT_GONE_UNREPORTED" ]; then
      _await_gone_refuse "the predicate '$pred' reported that it looked but never set AWAIT_GONE_ALIVE on look $AWAIT_GONE_LOOKS — an unset description defaults to the empty string, which is this poll's spelling of 'the subject is gone'"
      return 2
    fi

    # shellcheck disable=SC2034  # this IS the library's output: every caller
    # prints it on failure (await-gone_test.sh 142/204, gate-budget_test.sh
    # 358/412/452, swift-suite_test.sh 270), and line 20 of this file documents
    # it as the contract. Unused within this file by design.
    AWAIT_GONE_LAST="$AWAIT_GONE_ALIVE"
    if [ -z "$AWAIT_GONE_ALIVE" ]; then
      return 0
    fi
    if [ "$AWAIT_GONE_ELAPSED" -ge "$deadline" ]; then
      return 1
    fi
    sleep "$AWAIT_GONE_POLL_SECONDS"
  done
}
