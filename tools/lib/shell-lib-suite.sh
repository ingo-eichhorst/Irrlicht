#!/usr/bin/env bash
# shell-lib-suite.sh — run a directory's `*_test.sh` files so that "every one
# of them passed" and "the loop stopped early" cannot print the same thing
# (#1639).
#
# This file is sourced, not executed:
#
#   . tools/lib/shell-lib-suite.sh
#   rc=0
#   shell_lib_suite_run tools/lib posix-lint_test.sh || rc=$?
#   exit "$rc"
#
# Shell options, both directions, because the convention above depends on them
# and every other library here now says so (#1633, #1635):
#
#   this file  requires nothing of the caller's options and changes none of
#              them. shell_lib_suite_run RETURNS its verdict — 0, 1 or 2 —
#              whether or not `-e` is set, and runs every file it found
#              regardless of what any of them did.
#   the caller must keep `$?` reachable if it wants to distinguish the three.
#              Under `-e` a bare non-zero return aborts the caller on that
#              line, which is fine when the call is the step's last statement
#              but loses the code otherwise — write `|| rc=$?` (#1629).
#
# ---------------------------------------------------------------------------
# Why this exists
#
# There were two implementations of this loop and they disagreed about the one
# thing that matters. `.github/workflows/test.yml`'s step was:
#
#     for t in tools/lib/*_test.sh; do
#       case "$t" in */posix-lint_test.sh) continue ;; esac
#       echo "== $t =="
#       bash "$t"
#     done
#
# with no `|| rc=1`. That file declares no `shell:` and no `defaults:`, so the
# step runs under GitHub's documented default, `bash --noprofile --norc -eo
# pipefail`. The FIRST failing file aborted the step; every later file in glob
# order never ran, and NOTHING in the log said so — the output simply ended
# after that file's own `== header ==`. Measured, three fixtures with the first
# and the last failing: only the first one's header and body appear, and the
# step exits 1. A failure in `changed-files_test.sh` (first alphabetically)
# therefore hid the other eight, one round trip each.
#
# `tools/preflight.sh`'s `shell_lib_tests` — the local mirror of that same
# step — did the opposite (`bash "$t" || rc=1`) and ran all of them. So CI and
# the pre-push hook judged the same corpus by two implementations that could,
# and did, disagree. This is the one implementation both now call, for the
# reason `.github/workflows/macos-swift.yml` gives for sharing
# `tools/lib/swift-suite.sh`: "CI and the pre-push hook judge a run by the same
# rules rather than by two implementations that can disagree."
#
# The two callers still differ in SCOPE, deliberately: test.yml skips
# `posix-lint_test.sh`, which needs a static bashism linter the macos image
# does not ship (linux.yml runs it, and the gate it tests, for that reason).
# That difference is now an ARGUMENT rather than a second loop — and a skip
# naming a file that does not exist is a hard refusal (below), so it cannot
# silently stop matching the way a `case` pattern in a duplicated loop could.
#
# ---------------------------------------------------------------------------
# The empty-corpus refusal is not defensive noise
#
# Both old spellings got this wrong, in opposite directions, and both were
# measured rather than reasoned about:
#
#   test.yml's      `for t in dir/*_test.sh` with nullglob OFF (bash's default,
#                   and GitHub's `-eo pipefail` does not change it) iterates
#                   ONCE with the literal unexpanded pattern, so the step died
#                   with `bash: dir/*_test.sh: No such file or directory` and
#                   exit 127 — non-zero, but reading as a missing file rather
#                   than as "the corpus this gate exists to run is empty".
#   preflight's     `[[ -e "$t" ]] || continue` filtered that literal out and
#                   returned 0. A corpus that vanished passed SILENTLY, which
#                   is AGENTS.md's "absence of a finding and inability to look
#                   must never produce the same output" in its purest form.
#
# So finding nothing is a named refusal here, and the `[ -e ]` filter is kept
# so the answer is the same whether or not the caller has `nullglob` set.
#
# ---------------------------------------------------------------------------
# Exit statuses
#
#   0  every file that ran passed
#   1  at least one file FAILED — and every other file still ran, which is the
#      whole point
#   2  the run could not be judged at all: no directory, no corpus, a skip that
#      matches nothing, or a census that does not add up. Distinct from 1
#      because "nine tests, one red" and "nothing was checked" are different
#      answers, and 2 is also what makes this function drivable by
#      shell-lib-errexit_test.sh, whose recipes need a status a bare errexit
#      abort (always 1) cannot forge.

# The glob is a variable so the census message can name it, not so callers can
# change it — nothing passes one, and `*_test.sh` is the convention every
# caller and every test file in tools/lib/ already follows.
SHELL_LIB_SUITE_GLOB='*_test.sh'

# shell_lib_suite_run <dir> [skip-basename ...]
shell_lib_suite_run() {
  local dir="${1:-}"
  if [ $# -gt 0 ]; then shift; fi

  if [ -z "$dir" ]; then
    printf 'shell-lib-suite: refusing — no directory was named.\n' >&2
    return 2
  fi
  if [ ! -d "$dir" ]; then
    printf 'shell-lib-suite: refusing — %s is not a directory, so the corpus could not be read.\n' "$dir" >&2
    return 2
  fi

  # `[ -e ]` rather than a bare glob expansion: with nullglob OFF (the default,
  # and what GitHub's `bash --noprofile --norc -eo pipefail` runs with) an empty
  # directory yields the literal pattern, and with nullglob ON it yields
  # nothing. Both land on `found == 0` below, so the refusal does not depend on
  # a shell option the caller may or may not have set.
  local files=() f
  for f in "$dir"/$SHELL_LIB_SUITE_GLOB; do
    if [ -e "$f" ]; then files+=("$f"); fi
  done

  local found=${#files[@]}
  if [ "$found" -eq 0 ]; then
    printf 'shell-lib-suite: refusing — no %s files under %s. A corpus that vanished and a corpus that passed must not print the same thing.\n' \
      "$SHELL_LIB_SUITE_GLOB" "$dir" >&2
    return 2
  fi

  # The skip list, validated BEFORE anything runs. A skip that names no file is
  # a hard refusal rather than a no-op: the one thing a by-name exclusion must
  # never do is silently stop excluding — or silently stop being needed —
  # because from the log both look exactly like a clean run.
  local skiplist=$'\n' skip base matched
  for skip in ${1+"$@"}; do
    case "$skip" in
      '')
        printf 'shell-lib-suite: refusing — an empty skip name was passed.\n' >&2
        return 2 ;;
      */*)
        printf 'shell-lib-suite: refusing — the skip %s is a path; name a bare file name so the match is unambiguous.\n' "$skip" >&2
        return 2 ;;
    esac
    matched=0
    for f in "${files[@]}"; do
      if [ "${f##*/}" = "$skip" ]; then matched=1; fi
    done
    if [ "$matched" -eq 0 ]; then
      printf 'shell-lib-suite: refusing — the skip list names %s, which matches no file under %s. Either it was renamed or it is gone; a skip that stopped matching is indistinguishable from a clean run.\n' \
        "$skip" "$dir" >&2
      return 2
    fi
    skiplist="$skiplist$skip"$'\n'
  done

  local rc=0 ran=0 skipped=0 failed=0 failed_names="" skipped_names="" is_skip
  for f in "${files[@]}"; do
    base="${f##*/}"
    is_skip=0
    case "$skiplist" in
      *$'\n'"$base"$'\n'*) is_skip=1 ;;
    esac
    if [ "$is_skip" -eq 1 ]; then
      skipped=$((skipped + 1))
      skipped_names="$skipped_names $base"
      printf '== %s == SKIPPED by name\n' "$f"
      continue
    fi
    printf '== %s ==\n' "$f"
    # `if bash …` and not a bare call: errexit is suppressed for a command in
    # an `if` condition, so this is what lets a failing file be RECORDED
    # instead of aborting a caller that has `-e` on.
    if bash "$f"; then
      :
    else
      rc=1
      failed=$((failed + 1))
      failed_names="$failed_names $base"
    fi
    ran=$((ran + 1))
  done

  # The census. Printed on every path that got this far, pass or fail, because
  # a count of what was actually executed is the single line that tells a
  # truncated run from a clean one — which is the defect this file exists to
  # remove, and it had no line of its own in either predecessor.
  printf 'shell-lib-suite: %s — found %d, skipped %d (%s), ran %d, failed %d\n' \
    "$dir" "$found" "$skipped" "${skipped_names:-none}" "$ran" "$failed"
  if [ -n "$failed_names" ]; then
    printf 'shell-lib-suite: FAILED:%s\n' "$failed_names" >&2
  fi

  # ...and the census has to add up. This cannot catch an abort — an aborted
  # run never reaches this line at all, and that case is held instead by
  # shell-lib-errexit_test.sh, which drives this function under a caller's `-e`
  # and grades what it returns. What it DOES catch is the next edit to the loop
  # above: a `continue`, a `break` or an early `return` that skips a file
  # without counting it, which is exactly the shape of the defect being fixed.
  if [ $((ran + skipped)) -ne "$found" ]; then
    printf 'shell-lib-suite: refusing — the census does not add up: %d ran + %d skipped != %d found. The loop did not reach every file it discovered.\n' \
      "$ran" "$skipped" "$found" >&2
    return 2
  fi
  return "$rc"
}
