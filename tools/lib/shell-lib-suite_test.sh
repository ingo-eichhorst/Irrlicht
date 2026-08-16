#!/usr/bin/env bash
# shell-lib-suite_test.sh — the tests for tools/lib/shell-lib-suite.sh, the one
# implementation behind .github/workflows/test.yml's "Test the shared shell
# libs" step and tools/preflight.sh's `tools` gate (#1639).
#
# ---------------------------------------------------------------------------
# What is being asserted, and why each obligation exists
#
# The defect was a loop with no `|| rc=1` running under GitHub's default
# `bash --noprofile --norc -eo pipefail`: the first failing file aborted the
# step and every later file never ran, with nothing in the log distinguishing
# that from a clean run. So the obligations are, in order:
#
#   1. an EARLY failure and a LATE failure are BOTH reported. A fix that only
#      collects the status of the last file, or that reports "1 failed" when
#      two did, satisfies "the step goes red" and still costs a round trip.
#   2. the predecessors' behaviour is committed rather than described. Both old
#      spellings are emitted verbatim into an inner script and run under the
#      shell GitHub actually uses, so "the first failing file hides the rest"
#      and "an empty corpus passes silently" are re-measured on every run
#      instead of being true the day they were pasted into an issue. They are
#      also the vacuity guard for obligation 1: if bash ever stopped aborting
#      there, the new runner would be protecting nothing and would pass for the
#      wrong reason.
#   3. an empty corpus is a NAMED refusal, not a pass and not a `No such file`.
#   4. a skip that stops matching is a hard refusal. The scope difference
#      between the two callers (CI skips posix-lint_test.sh, which needs a
#      linter the macos image lacks) is an argument now, and an argument that
#      quietly matches nothing is the same silent-skip defect one level down.
#   5. the census adds up — driven against a DELIBERATELY MUTATED copy of the
#      library, because the guard is something this change ADDS and therefore
#      passes by construction until something breaks the thing it protects.
#   6. both call sites really do go through the shared runner, so the two
#      implementations cannot come back.
#
# The fixtures are built in a tempdir rather than committed under testdata/
# because they must be EXECUTED, and a corpus of deliberately-failing
# `*_test.sh` files under tools/lib/ would be picked up by the very glob under
# test — including by the real CI step. (tools/lib/testdata/ is excluded from
# the posix and skill gates' walks for the same class of reason, but the glob
# here is `tools/lib/*_test.sh`, which testdata/ does not protect against.)
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "FAIL: shell-lib-suite_test — cannot cd to $REPO_ROOT" >&2; exit 1; }

# A missing tool is a hard failure, not a skip: exiting 0 here would read as a
# PASS to the very runner under test, so this file would go green having
# asserted nothing — the failure mode the whole issue family is made of.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: shell-lib-suite_test — $1 not found" >&2; exit 1; }; }
need git
need mktemp
need sed
need grep

LIB=tools/lib/shell-lib-suite.sh
[[ -f "$LIB" ]] || { echo "FAIL: shell-lib-suite_test — $LIB not found" >&2; exit 1; }

TMP=$(mktemp -d -t shell-lib-suite) || exit 1
trap 'rm -rf "$TMP"' EXIT

rc=0
pass() { echo "  PASS: $1"; return 0; }
fail() { echo "  FAIL: $1 — expected [$2] got [$3]" >&2; rc=1; return 0; }
flat() { echo "$1" | tr '\n' ' '; }

want_status() { # label want got out
  if [[ "$3" == "$2" ]]; then pass "$1 (exit $2)"; else fail "$1" "exit $2" "exit $3 :: $(flat "$4")"; fi
  return 0
}
want_contains() { # label needle haystack
  case "$3" in *"$2"*) pass "$1" ;; *) fail "$1" "output containing: $2" "$(flat "$3")" ;; esac
  return 0
}
want_absent() { # label needle haystack
  case "$3" in *"$2"*) fail "$1" "no output containing: $2" "$(flat "$3")" ;; *) pass "$1" ;; esac
  return 0
}

# mkcorpus <dir> <spec...> — each spec is `name:0` (passes) or `name:1` (fails).
# Every fixture announces itself, so "did this file run" is answerable from the
# captured output rather than inferred from a status.
mkcorpus() {
  local dir="$1"; shift
  mkdir -p "$dir" || return 1
  local spec name status
  for spec in "$@"; do
    name="${spec%%:*}"; status="${spec##*:}"
    printf '#!/usr/bin/env bash\necho "RAN %s"\nexit %s\n' "$name" "$status" >"$dir/$name"
  done
  return 0
}

# run_suite <dir> [skip...] — the library, sourced and driven under the shell
# GitHub runs steps with, exactly as test.yml's step now does. Output lands in
# $OUT and the status in $ST.
run_suite() {
  local script="$TMP/run-suite.sh"
  SLS_ARGS_DIR="$1"; shift
  SLS_ARGS_SKIPS="$*"
  export SLS_ARGS_DIR SLS_ARGS_SKIPS
  cat >"$script" <<'RUNNER'
rc=0
. tools/lib/shell-lib-suite.sh
# Word-splitting $SLS_ARGS_SKIPS is intentional: it is this test's own list of
# bare file names, never user input.
# shellcheck disable=SC2086
shell_lib_suite_run "$SLS_ARGS_DIR" $SLS_ARGS_SKIPS || rc=$?
exit "$rc"
RUNNER
  OUT=$(bash --noprofile --norc -eo pipefail "$script" 2>&1); ST=$?
  return 0
}

# run_suite_bare <dir> [skip...] — the same call as the LAST STATEMENT of the
# inner script, with no `|| rc=$?` anywhere.
#
# This shape is not redundant with run_suite, it is the only one that can see
# the hazard. Bash suppresses errexit for a function's ENTIRE body when the
# call sits in a `||` (or `if`, or `&&`) position, so a library that ran its
# test files as bare `bash "$f"` statements — the defect being fixed, moved one
# level down — is invisible from run_suite and from test.yml's step, and would
# only surface the day a caller wrote the call bare. shell-lib-errexit_test.sh
# grades this position too, but it cannot grade it against a FAILING corpus:
# its obligation reads the inner shell's status, and a documented 1 is
# indistinguishable from an errexit abort, which also exits 1. Here the
# fixtures announce themselves, so "it returned 1" and "it aborted at the first
# red file" are told apart by whether the later files ran.
run_suite_bare() {
  local script="$TMP/run-suite-bare.sh"
  SLS_ARGS_DIR="$1"; shift
  SLS_ARGS_SKIPS="$*"
  export SLS_ARGS_DIR SLS_ARGS_SKIPS
  cat >"$script" <<'RUNNERBARE'
. tools/lib/shell-lib-suite.sh
# shellcheck disable=SC2086
shell_lib_suite_run "$SLS_ARGS_DIR" $SLS_ARGS_SKIPS
RUNNERBARE
  OUT=$(bash --noprofile --norc -eo pipefail "$script" 2>&1); ST=$?
  return 0
}

echo "== the runner reports every file, not just the ones before the first red =="

# --- the vacuity guard: a corpus that all passes -----------------------------
# Without it, a runner that reported failure unconditionally would satisfy
# every case below and read as excellent coverage.
mkcorpus "$TMP/clean" a_test.sh:0 b_test.sh:0 c_test.sh:0
run_suite "$TMP/clean"
want_status "a corpus that all passes" 0 "$ST" "$OUT"
want_contains "...and every file ran (a)" "RAN a_test.sh" "$OUT"
want_contains "...and every file ran (b)" "RAN b_test.sh" "$OUT"
want_contains "...and every file ran (c)" "RAN c_test.sh" "$OUT"
want_contains "...and the census says so" "found 3, skipped 0 (none), ran 3, failed 0" "$OUT"

# --- obligation 1: an EARLY red and a LATE red are both reported -------------
# a and c fail, b passes. Under the old spelling only a's own output survives;
# the fix has to produce all three, a failed count of 2, and BOTH names.
mkcorpus "$TMP/mixed" a_test.sh:1 b_test.sh:0 c_test.sh:1
run_suite "$TMP/mixed"
want_status "a corpus with an early AND a late failure" 1 "$ST" "$OUT"
want_contains "the early failing file ran" "RAN a_test.sh" "$OUT"
want_contains "the file BETWEEN the two failures ran" "RAN b_test.sh" "$OUT"
want_contains "the LATE failing file ran — the whole defect" "RAN c_test.sh" "$OUT"
want_contains "the census counts both failures" "found 3, skipped 0 (none), ran 3, failed 2" "$OUT"
want_contains "...and names them" "FAILED: a_test.sh c_test.sh" "$OUT"

# ...and the same corpus with the call in BARE STATEMENT position, where the
# caller's `-e` is live inside the function body. See run_suite_bare's comment:
# this is the only shape that can see the defect having moved one level down.
run_suite_bare "$TMP/mixed"
want_status "the same corpus, called as a bare statement under -e" 1 "$ST" "$OUT"
want_contains "bare position: the early failing file ran" "RAN a_test.sh" "$OUT"
want_contains "bare position: the file between them ran" "RAN b_test.sh" "$OUT"
want_contains "bare position: the late failing file ran too" "RAN c_test.sh" "$OUT"
want_contains "bare position: the census still printed" "found 3, skipped 0 (none), ran 3, failed 2" "$OUT"

# --- obligation 2: the predecessors, measured rather than described ----------
# test.yml's loop as it stood, verbatim, with only `tools/lib` replaced by the
# fixture dir — run under `bash --noprofile --norc -eo pipefail`, which is
# GitHub's documented invocation for a step declaring no `shell:`.
echo ""
echo "== the two predecessors, run under GitHub's own default shell =="
export SLS_DIR="$TMP/mixed"
cat >"$TMP/old-ci-loop.sh" <<'OLDCI'
for t in "$SLS_DIR"/*_test.sh; do
  case "$t" in */posix-lint_test.sh) continue ;; esac
  echo "== $t =="
  bash "$t"
done
OLDCI
old_out=$(bash --noprofile --norc -eo pipefail "$TMP/old-ci-loop.sh" 2>&1); old_st=$?
want_status "test.yml's old loop still goes red" 1 "$old_st" "$old_out"
want_contains "...having run the first file" "RAN a_test.sh" "$old_out"
# The load-bearing half of the pair. If bash ever stopped aborting here — a
# future runner spelling, a `defaults:` block someone adds — the new runner
# would be protecting nothing and every case above would pass for the wrong
# reason. Absence of the hazard and absence of the check must not look alike.
want_absent "...and the DEFECT is still real: the file after it never ran" "RAN b_test.sh" "$old_out"
want_absent "...nor the late failing one" "RAN c_test.sh" "$old_out"

# preflight's old loop over an EMPTY corpus: `[[ -e "$t" ]] || continue`
# filtered the literal unexpanded pattern out and returned 0 — a corpus that
# vanished passing silently, which is the same defect in its purest form.
mkdir -p "$TMP/empty"
export SLS_EMPTY="$TMP/empty"
cat >"$TMP/old-preflight-loop.sh" <<'OLDPF'
rc=0
for t in "$SLS_EMPTY"/*_test.sh; do
  [[ -e "$t" ]] || continue
  bash "$t" || rc=1
done
exit "$rc"
OLDPF
oldpf_out=$(bash --noprofile --norc -eo pipefail "$TMP/old-preflight-loop.sh" 2>&1); oldpf_st=$?
want_status "preflight's old loop passed an EMPTY corpus silently" 0 "$oldpf_st" "$oldpf_out"
want_absent "...saying nothing at all about it" "found" "$oldpf_out"

# ...and CI's old loop over the same empty corpus did not pass, but did not say
# what was wrong either: nullglob is OFF by default (and `-eo pipefail` does not
# change it), so the loop iterated once with the literal pattern and died on a
# missing file. Non-zero, and unattributable.
cat >"$TMP/old-ci-empty.sh" <<'OLDCIE'
for t in "$SLS_EMPTY"/*_test.sh; do
  case "$t" in */posix-lint_test.sh) continue ;; esac
  echo "== $t =="
  bash "$t"
done
OLDCIE
oldcie_out=$(bash --noprofile --norc -eo pipefail "$TMP/old-ci-empty.sh" 2>&1); oldcie_st=$?
want_status "CI's old loop over an empty corpus died on the literal glob" 127 "$oldcie_st" "$oldcie_out"
want_contains "...reading as a missing file, not as an empty corpus" "No such file or directory" "$oldcie_out"

# --- obligation 3: the empty corpus is a named refusal ----------------------
echo ""
echo "== a run that could not be judged refuses with 2, and says why =="
run_suite "$TMP/empty"
want_status "an empty corpus" 2 "$ST" "$OUT"
want_contains "...names the glob and the directory" "no *_test.sh files under $TMP/empty" "$OUT"
want_contains "...and says why that is not a pass" "must not print the same thing" "$OUT"

run_suite "$TMP/there-is-no-such-directory"
want_status "a directory that does not exist" 2 "$ST" "$OUT"
want_contains "...is refused by name" "is not a directory" "$OUT"

# --- obligation 4: the skip cannot silently stop matching -------------------
echo ""
echo "== the by-name skip =="
mkcorpus "$TMP/skip" a_test.sh:0 b_test.sh:0 c_test.sh:0
run_suite "$TMP/skip" b_test.sh
want_status "a skip that matches" 0 "$ST" "$OUT"
want_absent "...excludes exactly that file" "RAN b_test.sh" "$OUT"
want_contains "...runs its neighbours" "RAN a_test.sh" "$OUT"
want_contains "...and the census accounts for it" "found 3, skipped 1 (b_test.sh), ran 2, failed 0" "$OUT"

# The mutation for obligation 4: the same call with a name nothing matches.
# This is what a rename or a deletion of posix-lint_test.sh looks like from
# test.yml, and it must be loud — in the old `case "$t" in */posix-lint_test.sh)`
# spelling it was a pattern that simply stopped matching, i.e. silence.
run_suite "$TMP/skip" was-renamed_test.sh
want_status "a skip that matches nothing" 2 "$ST" "$OUT"
want_contains "...names it" "the skip list names was-renamed_test.sh" "$OUT"
want_contains "...and says why silence would be worse" "indistinguishable from a clean run" "$OUT"

run_suite "$TMP/skip" sub/dir_test.sh
want_status "a skip spelled as a path" 2 "$ST" "$OUT"
want_contains "...is refused rather than matched loosely" "is a path" "$OUT"

# --- obligation 5: the census guard, against a mutated library --------------
#
# This guard is something the change ADDS, so it passes by construction and
# owes a deliberate mutation (AGENTS.md). The mutation is committed here rather
# than described in a PR body: a copy of the real library with an uncounted
# `continue` spliced into its run loop — the exact shape of a future edit that
# skips a file without accounting for it. The real file is never touched.
echo ""
echo "== the census guard, driven against a deliberately mutated copy =="
mutdir="$TMP/mutlib"
mkdir -p "$mutdir"
# Both patterns below are LITERAL text matched against the library's source —
# `$f` and `${f##*/}` must reach sed and grep unexpanded, which is the whole
# point of the single quotes.
# shellcheck disable=SC2016
sed 's|^    printf .== %s ==.n. "\$f"$|    if [ "${f##*/}" = b_test.sh ]; then continue; fi\n&|' \
  "$LIB" >"$mutdir/shell-lib-suite.sh"
# shellcheck disable=SC2016
if ! grep -q 'if \[ "${f##\*/}" = b_test.sh \]; then continue; fi' "$mutdir/shell-lib-suite.sh"; then
  fail "the census mutation was applied" "a spliced uncounted continue" "the splice matched nothing — the mutation harness is stale, not the library clean"
else
  pass "the census mutation was applied to the copy"
  export SLS_MUT="$mutdir/shell-lib-suite.sh" SLS_MUT_DIR="$TMP/clean"
  cat >"$TMP/run-mut.sh" <<'MUT'
rc=0
. "$SLS_MUT"
shell_lib_suite_run "$SLS_MUT_DIR" || rc=$?
exit "$rc"
MUT
  mut_out=$(bash --noprofile --norc -eo pipefail "$TMP/run-mut.sh" 2>&1); mut_st=$?
  want_status "a loop that skips a file without counting it" 2 "$mut_st" "$mut_out"
  want_contains "...is refused by the census" "the census does not add up: 2 ran + 0 skipped != 3 found" "$mut_out"
fi
# ...and the unmutated library over the same corpus must NOT refuse, or the
# case above would be satisfied by a guard that fires unconditionally.
run_suite "$TMP/clean"
want_absent "...and the real library over the same corpus does not" "census does not add up" "$OUT"

# --- obligation 6: both call sites go through the shared runner -------------
#
# Not a general workflow linter, and the scope is the honest part: this is a
# lock on the TWO call sites that exist, so the duplication #1639 removed
# cannot come back unnoticed. It reads the files rather than running them.
echo ""
echo "== both callers really do call the one implementation =="
WF=.github/workflows/test.yml
if [[ ! -f "$WF" ]]; then
  fail "the call-site check could run" "$WF" "not found"
else
  # Vacuity guard first: a scan that stopped finding the step reads exactly
  # like a workflow with no duplication in it.
  step=$(awk '/^      - name: Test the shared shell libs$/{found=1} found{print}' "$WF")
  if [[ -z "$step" ]]; then
    fail "the scan finds the step in $WF" "a 'Test the shared shell libs' step" "no such step — the scan has gone blind, not the workflow clean"
  else
    pass "the scan reads $WF's 'Test the shared shell libs' step"
    want_contains "...and it calls the shared runner" "shell_lib_suite_run tools/lib posix-lint_test.sh" "$step"
    want_absent "...rather than carrying its own loop" 'for t in tools/lib/*_test.sh' "$step"
    want_contains "...keeping \$? reachable under GitHub's implicit -e" '|| rc=$?' "$step"
  fi
fi

PF=tools/preflight.sh
if [[ ! -f "$PF" ]]; then
  fail "the call-site check could run" "$PF" "not found"
else
  pf=$(cat "$PF")
  want_contains "$PF sources the shared runner" 'lib/shell-lib-suite.sh' "$pf"
  want_contains "...and its gate calls it" "shell_lib_suite_run tools/lib" "$pf"
  want_absent "...rather than carrying its own loop" 'for t in tools/lib/*_test.sh' "$pf"

  # ...and the `tools` gate has to FIRE on a diff that touches only test.yml,
  # or under --changed (the pre-push hook's path) the workflow assertions above
  # are skipped on precisely the commit that can break them. That is #1591's
  # and #1629's shape, both of which were fixed by widening this same trigger.
  # The regex is EXTRACTED and matched rather than string-compared, so it is a
  # behavioural assertion and not a lock on one spelling of an alternation.
  tools_re=$(grep -a "run_gate_scoped '\^tools/lib/" "$PF" \
             | sed -E "s/^[[:space:]]*run_gate_scoped '//; s/'[[:space:]]*\\\\?[[:space:]]*$//")
  if [[ -z "$tools_re" ]]; then
    fail "the tools-gate trigger regex could be read from $PF" "one run_gate_scoped line starting ^tools/lib/" "no such line — the scan has gone blind, not the trigger wrong"
  else
    pass "read the tools-gate trigger regex from $PF"
    for probe in .github/workflows/test.yml tools/lib/shell-lib-suite_test.sh; do
      if printf '%s\n' "$probe" | grep -qE "$tools_re"; then
        pass "...it fires on a diff touching $probe"
      else
        fail "the tools gate fires on a diff touching $probe" "a match" "no match against: $tools_re"
      fi
    done
    # The vacuity guard: a trigger that matched everything would satisfy both
    # probes above and scope nothing.
    if printf '%s\n' core/domain/session.go | grep -qE "$tools_re"; then
      fail "the tools-gate trigger still scopes" "no match for core/domain/session.go" "it matches everything: $tools_re"
    else
      pass "...and still does not fire on an unrelated core/ file"
    fi
  fi
fi

# The skip CI passes has to name a file that is really there. The library
# refuses at runtime if it does not — this is the same assertion one push
# earlier, on the machine that can act on it.
if [[ -e tools/lib/posix-lint_test.sh ]]; then
  pass "the skip test.yml passes names a file that exists (tools/lib/posix-lint_test.sh)"
else
  fail "the skip test.yml passes names a real file" "tools/lib/posix-lint_test.sh" "missing — CI's step will refuse with status 2 until the skip is updated"
fi

echo ""
if [[ "$rc" -eq 0 ]]; then
  echo "shell-lib-suite_test: ALL PASS"
else
  echo "shell-lib-suite_test: FAILURES" >&2
fi
exit "$rc"
