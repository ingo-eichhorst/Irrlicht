#!/usr/bin/env bash
# checker-assert.sh — the shared `assert_checker_rc` helper for the tests that
# drive a tools/lib checker function and grade its exit status plus one
# substring of its output.
#
# WHY THIS EXISTS (#2022, and #1823 before it). tools/lib/mutation-assert.sh
# was created because "this exact ~40-line function was copy-pasted
# byte-for-byte into two new fixture files added by the same diff" (its own
# header). #2022's first draft did the same thing one layer up: the
# `assert_rc` helpers in fleet-scope-overlap_test.sh and
# fleet-review-evidence_test.sh were byte-identical apart from the callee's
# name, and so was the hand-rolled "no files named must refuse with 2" block
# beneath each. Passing the function as the first argument removes both.
#
# Sourced, not executed:
#
#   . tools/lib/checker-assert.sh
#   rc=0
#   assert_checker_rc fleet_scope_overlap '<label>' 0 'DISJOINT' a.md b.md
#   exit "$rc"
#
# Requires the caller to have declared `rc` as a plain integer. This function
# sets it in the caller's own shell (no subshell), the way mutation-assert.sh
# accumulates into `fails`, so results add up across every call.
#
# NOT used by tools/lib/ref-exists_test.sh. That file's wrapper prefixes every
# call with a stub `PATH` and cannot share this signature without pushing the
# stub into the helper, where the other two callers would have no use for it.
# A third near-copy would have been worth extracting; a helper with an
# argument only one caller ever sets is not.
set -uo pipefail

# assert_checker_rc <fn> <label> <expected-rc> <must-contain> [arg...]
#
# Runs `<fn> [arg...]` with stderr folded into stdout, and requires both the
# exit status and, when <must-contain> is non-empty, a fixed-string match in
# the output. A zero-argument call is written by simply passing no [arg...],
# which is the "no files named" refusal case.
assert_checker_rc() {
  local fn="$1" label="$2" want="$3" needle="$4"
  shift 4

  # `out=$(...)` alone is an errexit trigger: the assignment carries the
  # substitution's status, so under a caller's `set -e` this function aborted
  # the caller the moment the checker returned its documented 1 or 2 — which
  # is the entire point of driving a checker. tools/lib/shell-lib-errexit_test.sh
  # caught it. The `&& ... || ...` form makes it a compound command, where
  # errexit does not apply.
  local out got
  out=$("$fn" "$@" 2>&1) && got=0 || got=$?

  if [ "$got" -ne "$want" ]; then
    echo "FAIL: $label — wanted exit $want, got $got" >&2
    echo "$out" | sed 's/^/      | /' >&2
    # shellcheck disable=SC2034  # rc is the CALLER's accumulator, declared there
    rc=1
    return 1
  fi

  if [ -n "$needle" ] && ! grep -qF "$needle" <<<"$out"; then
    echo "FAIL: $label — exit $got was right, but the output never said '$needle'" >&2
    echo "$out" | sed 's/^/      | /' >&2
    # shellcheck disable=SC2034  # rc is the CALLER's accumulator, declared there
    rc=1
    return 1
  fi

  echo "  PASS: $label (exit $got)"
  return 0
}
