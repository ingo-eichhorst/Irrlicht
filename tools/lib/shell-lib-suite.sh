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
# A caller that also needs to hand-run a file OUTSIDE the globbed directory
# (smoke-test.sh's three replaydata/ suites are the reason this exists) goes
# through `shell_lib_suite_exec <file>` instead of a bare `bash "$file"`, so
# that file joins the same run-scoped ledger `shell_lib_suite_run` populates —
# see shell_lib_suite_exec's own header below for why (#1828).
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
# step runs under GitHub's documented default for such a step, `bash -e {0}` —
# errexit ONLY, no pipefail (measured off run 31960152598's own step header;
# `bash --noprofile --norc -eo pipefail` is what `shell: bash` gives, and this
# comment claimed it until #1650). The FIRST failing file aborted the step,
# which `-e` alone is enough to do; every later file in glob
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
#                   and neither GitHub's `-e` nor `shell: bash`'s added
#                   `-o pipefail` changes it) iterates
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
#      matches nothing, a census that does not add up, or the same file asked
#      to run twice in this process (#1828 — see shell_lib_suite_exec below).
#      Distinct from 1 because "nine tests, one red" and "nothing was checked"
#      are different answers, and 2 is also what makes this function drivable
#      by shell-lib-errexit_test.sh, whose recipes need a status a bare
#      errexit abort (always 1) cannot forge.

# The glob is a variable so the census message can name it, not so callers can
# change it — nothing passes one, and `*_test.sh` is the convention every
# caller and every test file in tools/lib/ already follows.
SHELL_LIB_SUITE_GLOB='*_test.sh'

# ---------------------------------------------------------------------------
# The once-per-process ledger (#1828)
#
# PR #1827's own defect: `shell_lib_suite_run "$SCRIPT_DIR/lib"` already runs
# every file the glob finds, and a few lines below that call smoke-test.sh
# used to carry hand-written `bash "$SCRIPT_DIR/lib/<name>_test.sh" || rc=1`
# lines for two of the SAME files — re-added after #1803's cleanup removed
# them once already. Both suites ran twice for a full PR cycle: two
# `shell-lib-suite:` census lines, two `ALL PASS`, and nothing in the log said
# "this file already ran" — a reviewer caught it by counting PASS lines by
# hand. `shell_lib_suite_run` already refuses an empty corpus, a skip that
# matches nothing, and a census that does not add up; none of those catch a
# file that runs a second time as a SEPARATE statement outside the loop, so
# this ledger is what does.
#
# It is a plain `\n`-delimited string, not an associative array, for the same
# reason the skip list a few lines below is one: this file is sourced under
# whatever bash the caller has, and `declare -A` needs bash 4+, which macOS's
# stock /bin/bash (3.2.57) is not — the same constraint tools/lib/gate-budget.sh's
# header states, and every caller of THIS file runs under that shell at least
# once (test.yml's step declaring no `shell:`, and any developer's
# `tools/preflight.sh`).
#
# It lives at FILE scope, not inside a function, so it is run-scoped rather
# than call-scoped: it survives across multiple `shell_lib_suite_run` calls
# and hand `shell_lib_suite_exec` calls in the same process, which is exactly
# the shape smoke-test.sh needs — one globbed suite under lib/, plus three
# hand-run files that live outside it and must never be flagged as duplicates
# of one another or of anything the glob found.
SHELL_LIB_SUITE_LEDGER=$'\n'

# Whether the LAST shell_lib_suite_exec call refused structurally, as opposed
# to running a file that then failed. It exists because exit status alone
# cannot carry both answers: exec returns what `bash "$file"` returned, and a
# test file is free to exit 2 itself. Reading 2 as "the ledger refused" made a
# single such file abort the whole run before its census line — a truncated
# run that reads exactly like a clean one, which is the defect this file was
# written to remove. The flag is set ONLY on the refusal paths, so the caller
# asks the right question instead of inferring it from a shared number.
SHELL_LIB_SUITE_EXEC_REFUSED=0

# shell_lib_suite_exec <file> — run ONE test file, recording it into the
# process-wide ledger above and refusing a second execution of the same
# (canonicalized) file rather than silently running it twice. Returns what
# `bash "$file"` returns (0 or non-zero) on a first run, or 2 on a structural
# refusal (an empty name, a path that cannot be canonicalized, or a repeat) —
# the same "2 means this could not be judged, not that it failed" convention
# `shell_lib_suite_run` uses.
#
# `shell_lib_suite_run`'s own loop calls this for every file it globs, so a
# file discovered by the glob and ALSO hand-run a few lines below the call
# (#1827's exact shape) is caught the moment the hand-run line executes —
# whether that line comes before or after the `shell_lib_suite_run` call in
# the caller's script, since the ledger does not care about order, only about
# whether a canonical path has been seen before in this process.
#
# Canonicalization is done HERE, inline, rather than through a separate
# helper: `$SCRIPT_DIR/lib/x_test.sh` and `$SCRIPT_DIR/../scripts/lib/x_test.sh`
# must compare equal in the ledger above, and doing that needs an absolute,
# symlink-resolved path — deliberately NOT `realpath` or `readlink -f`, since
# the stock macOS `/bin/readlink` has no `-f`, `realpath` is not guaranteed to
# be on PATH, and a canonicalizer that refuses on a bare macOS box would make
# the ledger unusable on the exact machine most of this file's own header
# incidents were measured on. `cd … && pwd -P` resolves symlinks and relative
# components using nothing but a builtin and a POSIX utility.
#
# A path whose DIRECTORY cannot be entered (renamed, never existed, a typo)
# is refused rather than answered with the un-resolved string: two different
# spellings of a directory that does not exist would canonicalize to two
# different bogus strings and the ledger would never catch the duplicate it
# exists to catch. Per AGENTS.md — a verification mechanism must fail loudly
# when it cannot run — silently falling back here would be indistinguishable
# from "this file never ran before", which is the one answer that must never
# be given by mistake.
shell_lib_suite_exec() {
  local file="${1:-}" raw_dir dir base canon
  SHELL_LIB_SUITE_EXEC_REFUSED=0
  if [ -z "$file" ]; then
    SHELL_LIB_SUITE_EXEC_REFUSED=1
    printf 'shell-lib-suite: refusing — exec was asked to run an empty file name.\n' >&2
    return 2
  fi
  raw_dir=$(dirname "$file")
  base=${file##*/}
  if ! dir=$(cd "$raw_dir" 2>/dev/null && pwd -P); then
    printf 'shell-lib-suite: refusing — %s could not be canonicalized: %s does not exist or cannot be entered, so the ledger cannot tell whether this file already ran.\n' \
      "$file" "$raw_dir" >&2
    SHELL_LIB_SUITE_EXEC_REFUSED=1
    return 2
  fi
  canon="$dir/$base"
  case "$SHELL_LIB_SUITE_LEDGER" in
    *$'\n'"$canon"$'\n'*)
      printf 'shell-lib-suite: refusing — %s already ran once in this process (resolved to %s). A file run twice and a file run once must not both read as one clean pass — the second run means a duplicate was re-added somewhere, not that there is a second data point.\n' \
        "$file" "$canon" >&2
      SHELL_LIB_SUITE_EXEC_REFUSED=1
      return 2
      ;;
  esac
  SHELL_LIB_SUITE_LEDGER="$SHELL_LIB_SUITE_LEDGER$canon"$'\n'
  bash "$file"
}

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
  # and what both of GitHub's invocations — `bash -e` for a step declaring
  # nothing, `bash --noprofile --norc -e -o pipefail` for `shell: bash` — run
  # with) an empty
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
    # `if shell_lib_suite_exec …` and not a bare call, for the same errexit
    # reason the old `if bash "$f"` comment gave. Going through
    # shell_lib_suite_exec (rather than calling `bash "$f"` here directly) is
    # what makes a hand-run duplicate of THIS file, added anywhere else in the
    # same process, a refusal instead of a silent second pass (#1828).
    if shell_lib_suite_exec "$f"; then
      :
    else
      if [ "$SHELL_LIB_SUITE_EXEC_REFUSED" -eq 1 ]; then
        # A structural refusal from the ledger or the canonicalizer, not a
        # test failure — shell_lib_suite_exec already said why, to stderr.
        # Gated on the flag and NOT on exec_rc: a test file may exit 2 on its
        # own, and reading that as a refusal aborted the run before its
        # census (#1828).
        # Returned immediately, before the census: a run that could not be
        # judged does not get to print one, same as the empty-corpus and
        # skip-list refusals above.
        return 2
      fi
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
  skipped_names="${skipped_names# }"
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
