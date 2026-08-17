#!/usr/bin/env bash
# bash-lint_test.sh — unit tests for tools/bash-lint.sh. Plain bash (no
# framework), matching the style of lib/posix-lint_test.sh. Run directly, or via
# linux.yml's "Test the bash-lint gate" step. Exits non-zero on any failed
# assertion.
#
# Covers issue #1684. Every bash file in this repo was outside every static
# linter it runs — 23 files / 8,431 lines under tools/lib/ alone, which is the
# machinery deciding whether every OTHER gate passes. It is the same
# file-selection blindness class as #1423 and #1611: a gate whose selection rule
# excludes a whole family reads exactly like a gate that passed.
#
# THE POINT OF THIS FILE is that the new gate can be shown RED. Per AGENTS.md's
# rule for gates that pass by construction, the deliberate mutation is the
# evidence — so rather than a scratch mutation that disappears with the PR, the
# corpus is committed under testdata/bash-lint/ and asserted here, one rule
# class per fixture.
#
# `good-clean.sh` carries as much weight as the nine broken fixtures: it is the
# vacuity guard. A linter that failed every input would satisfy all nine and be
# worthless, and only the clean fixture tells those two apart.
# `style-noisy-but-warning-clean.sh` pins the severity floor in the one
# direction the bad-* files cannot reach.
#
# The refusal cases are the other half, and they are the paths by which this
# gate could itself become the thing it was built to remove — a green check
# that ran nothing: no shellcheck, an empty file set, an exclusion rule that
# has stopped matching, and a shellcheck that could not check.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

NAME="bash-lint_test"
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
LINT="$ROOT/tools/bash-lint.sh"
FIXTURES="$DIR/testdata/bash-lint"

fails=0
pass() { echo "  PASS: $1"; return 0; }
fail() {
  echo "  FAIL: $1 — expected [$2] got [$3]"
  fails=$((fails + 1))
  return 0
}
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] && pass "$label" || fail "$label" "$expected" "$actual"
  return 0
}
assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  [[ "$haystack" == *"$needle"* ]] && pass "$label" \
    || fail "$label" "contains '$needle'" "${haystack:0:300}"
  return 0
}
assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  [[ "$haystack" != *"$needle"* ]] && pass "$label" \
    || fail "$label" "does NOT contain '$needle'" "${haystack:0:300}"
  return 0
}

# stub_path <dir> [extra-tool ...] — a PATH holding only the tools the gate
# needs to RUN, so a case can remove exactly one thing and isolate it. `sh` and
# `bash` stay reachable deliberately: the wrong outcome for a missing linter is
# the gate degrading to something weaker and calling that a check.
stub_path() {
  local d="$1"; shift
  mkdir -p "$d" || return 1
  local t real
  for t in sh bash git awk sed basename dirname sort cat mktemp printf "$@"; do
    real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$d/$t"
  done
  return 0
}

echo "== $NAME =="

# ===========================================================================
# 0. The gate needs shellcheck. Without it every assertion below would be
#    grading the refusal path instead of the detection path, and a suite that
#    quietly stopped checking detection is this issue wearing a new mask. So
#    say plainly whether it is there, and let cases 8-9 (which REMOVE it on
#    purpose) be the only ones that run without it.
# ===========================================================================
if ! command -v shellcheck >/dev/null 2>&1; then
  # Not a skip-to-green. Reported as a failure of the ENVIRONMENT so it cannot
  # be mistaken for the corpus passing. CI runs this on ubuntu-latest, whose
  # image ships shellcheck 0.9.0.
  echo "  FAIL: environment — shellcheck not found, so the detection path cannot be tested here" >&2
  echo "        install: brew install shellcheck  |  apt-get install -y shellcheck" >&2
  echo "$NAME: 1 FAILED" >&2
  exit 1
fi
pass "environment: shellcheck $(shellcheck --version | awk '/^version:/ {print $2}') is available"

# ===========================================================================
# 1. Every rule class must be CAUGHT. One fixture per class; the class is in
#    the filename so a red run names the construct, not just a path.
#
#    These are the mutations. Against the state of the repo before #1684 all
#    nine PASS, because nothing read a bash file at all.
# ===========================================================================
for fixture in "$FIXTURES"/bad-*.sh; do
  base="$(basename "$fixture")"
  out=$("$LINT" "$fixture" 2>&1)
  rc=$?
  assert_eq "detect: $base is rejected (exit 1)" "1" "$rc"
  # Exit status alone is satisfied by the gate erroring for an unrelated reason
  # (a refusal exits 2, but a future refactor could exit 1). Pin the file's own
  # name on a FAIL line so the rejection is about THIS file.
  assert_contains "detect: $base is named on a FAIL line" "FAIL $fixture" "$out"
done

# ===========================================================================
# 2. The vacuity guard: clean bash must PASS. Without this, a linter that
#    rejected every input would satisfy all nine assertions above.
# ===========================================================================
out=$("$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "vacuity: good-clean.sh passes (exit 0)" "0" "$rc"
assert_contains "vacuity: good-clean.sh reports ALL PASS" "ALL PASS" "$out"
assert_contains "vacuity: ...and says it actually read the file" "ok  $FIXTURES/good-clean.sh" "$out"

# ===========================================================================
# 3. The severity floor, in the only direction the bad-* fixtures cannot
#    reach. They all trip a warning or an error, so they exercise the "report
#    it" arm; this fixture makes shellcheck exit 1 carrying ONLY findings BELOW
#    the floor (SC2086, SC2012), which the gate must discard.
#
#    Without it, lowering the floor to `style` leaves the whole suite green
#    while 199 findings land across the corpus, 163 of which CI's 0.9.0 and a
#    local 0.11.0 disagree about — see tools/bash-lint.sh's header.
# ===========================================================================
out=$("$LINT" "$FIXTURES/style-noisy-but-warning-clean.sh" 2>&1)
rc=$?
assert_eq "floor: style-noisy but warning-clean still passes (exit 0)" "0" "$rc"
assert_contains "floor: the run names the severity it applied" "--severity=warning" "$out"

# ===========================================================================
# 4. THE SILENT ONE. `bad-directive-prose.sh` carries an SC2115 that shellcheck
#    never reports, because a comment line opening with the linter's own name is
#    parsed as a directive and an unparseable directive ABANDONS the file.
#
#    Asserting only "the fixture is rejected" (case 1 already does) would not
#    show that. This case rewords that ONE line and asserts the hidden finding
#    appears — which is the whole reason SC1072/SC1073 must be inside the floor
#    rather than filtered away as a parse nit. It is the same argument
#    posix-lint.sh makes with PARSE_ABORT_CODES, and the same shape as
#    skill-lint.sh's rule that a validator which cannot parse its input checks
#    MORE, never less.
#
#    `replaydata/_lib/drive/contracts.sh` carries this construct today (outside
#    this gate's scope; filed as #1687), and tools/bash-lint.sh's own header
#    tripped it on the gate's first run.
# ===========================================================================
prose_tmp="$(mktemp -d)"
hidden="$prose_tmp/reworded.sh"
if sed '/^# shellcheck this line is prose/s/^# shellcheck /# NOTE: /' \
      "$FIXTURES/bad-directive-prose.sh" > "$hidden"; then
  # The rewording has to have HAPPENED. A sed that matched nothing would leave
  # the file identical, the assertion below would fail, and the failure would
  # point at the gate instead of at this test.
  if cmp -s "$hidden" "$FIXTURES/bad-directive-prose.sh"; then
    fail "abandonment: the fixture's directive line was reworded" "a changed file" "byte-identical — the sed matched nothing"
  else
    pass "abandonment: the fixture's directive line was reworded"
    out=$("$LINT" "$hidden" 2>&1)
    rc=$?
    assert_eq "abandonment: the reworded copy is still rejected" "1" "$rc"
    assert_contains "abandonment: ...and NOW the hidden SC2115 is reported" "SC2115" "$out"
    # And the original must NOT report it — that asymmetry is the finding.
    orig=$("$LINT" "$FIXTURES/bad-directive-prose.sh" 2>&1)
    assert_not_contains "abandonment: the original hides the SC2115 entirely" "SC2115" "$orig"
    assert_contains "abandonment: ...reporting only the unparseable directive" "SC1073" "$orig"
  fi
else
  fail "abandonment: the fixture could be copied" "a copy under $prose_tmp" "sed failed"
fi
rm -rf "$prose_tmp"

# ===========================================================================
# 5. Discovery. The gate must find the repo's real bash and must NOT walk its
#    own deliberately-broken corpus — if it did, CI would be permanently red
#    and the corpus would have to be deleted, taking the evidence with it.
# ===========================================================================
out=$("$LINT" 2>&1)
rc=$?
assert_eq "discovery: the repo's real bash scripts pass (exit 0)" "0" "$rc"
assert_contains "discovery: tools/preflight.sh is in scope" "ok  tools/preflight.sh" "$out"
assert_contains "discovery: tools/lib/gate-budget.sh is in scope" "ok  tools/lib/gate-budget.sh" "$out"
assert_contains "discovery: the gate lints ITSELF" "ok  tools/bash-lint.sh" "$out"
assert_not_contains "discovery: the fixture corpus is excluded" "ok  tools/lib/testdata/" "$out"

# The census is what tells "it linted the corpus" from "it excluded the corpus
# and passed". Both numbers are printed, so assert the shape of the line rather
# than a count that would need editing on every new script.
assert_contains "discovery: the census names both halves" "bash-lint: " "$out"
assert_contains "discovery: ...including the testdata exclusion's own tally" "*/testdata/*=" "$out"
assert_contains "discovery: ...and the recording-rig exclusion's" "replaydata/*=" "$out"

# The excluded families must be genuinely absent from the linted list, not
# merely uncounted.
assert_not_contains "discovery: replaydata drivers are excluded" "ok  replaydata/" "$out"

# ...and the exclusions must not have swallowed the corpus. `--list` is the
# machine-readable form of the census; a scope that collapsed to a handful of
# files would satisfy every assertion above.
# `wc -l`, not `grep -c <pattern>`: the default `grep` on a developer machine
# here is ugrep, which does not take GNU's BRE `\|` alternation, so a
# pattern-based count silently collapsed to 1 — a check that reports a number
# nothing produced, which is this section's own subject.
listed=$("$LINT" --list 2>&1)
listed_n=$(printf '%s\n' "$listed" | grep -c .)
if [[ "$listed_n" -ge 60 ]]; then
  pass "discovery: --list reports $listed_n in-scope files (a corpus, not a handful)"
else
  fail "discovery: --list reports a real corpus" "at least 60 files" "$listed_n"
fi

# ===========================================================================
# 6. UNTRACKED FILES. `git ls-files` alone lists index entries only, which is
#    #1611's defect: since #1609 a brand-new untracked script is exactly the
#    file that puts a lint gate in scope via `tools/preflight.sh --changed`, and
#    a gate walking the index could not see the file that summoned it.
#
#    These cases cannot use the fixtures above — those are TRACKED, which is
#    the property under test — so each builds a throwaway git repo.
# ===========================================================================

# scratch_repo <dir> — a throwaway repo holding a copy of the gate at the path
# it derives its root from (<root>/tools/bash-lint.sh, via $0), one tracked
# clean bash script, and one file per declared exclusion so the gate's
# exclusion-existence refusal is satisfied. Callers plant the untracked file
# whose treatment is under test, so the census below is a full one.
scratch_repo() {
  local d="$1"
  rm -rf "$d" && mkdir -p "$d/tools" || return 1
  cp "$LINT" "$d/tools/bash-lint.sh" || return 1
  (
    cd "$d" || exit 1
    git init -q . || exit 1
    printf '#!/usr/bin/env bash\nset -eu\necho clean\n' > tracked-clean.sh || exit 1
    # One inhabitant per exclusion rule. Without these the gate refuses (case
    # 7), which would make every assertion here grade the wrong path.
    mkdir -p tools/lib/testdata/bash-lint replaydata/_lib tools/templates || exit 1
    printf '#!/usr/bin/env bash\nfoo=1\n' > tools/lib/testdata/bash-lint/fixture.sh || exit 1
    printf '#!/usr/bin/env bash\nbar=1\n' > replaydata/_lib/driver.sh || exit 1
    printf '#!/usr/bin/env bash\nbaz=1\n' > tools/templates/drive.sh.tmpl || exit 1
    git add -A . || exit 1
    # The fixture IS the test here. A scratch repo that did not come up would
    # make every assertion below grade a tree git cannot read, and a gate that
    # found nothing to lint is indistinguishable from one that found nothing
    # wrong — the shape this whole file exists to refuse.
    [[ -n "$(git ls-files)" ]] || exit 1
  ) || return 1
}

untracked_tmp="$(mktemp -d)"
scratch="$untracked_tmp/repo"

# 6a. THE MUTATION. An untracked bash file carrying a finding must make the gate
#     FAIL and must be NAMED. Without this the untracked walk is unfalsifiable:
#     the gate passes on a clean tree either way.
if scratch_repo "$scratch"; then
  printf '#!/usr/bin/env bash\nset -u\nd=""\nrm -rf "$d/lib"\n' > "$scratch/untracked-bad.sh"
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a finding in an UNTRACKED script fails the gate" "1" "$rc"
  assert_contains "untracked: the untracked file is named on a FAIL line" \
    "FAIL untracked-bad.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 6b. The direction that fails SILENTLY, and therefore the one needing the
#     stronger assertion: a clean untracked script must pass AND must have been
#     SEEN. Exit 0 alone is satisfied by never looking at it, which is the
#     defect. So pin the per-file `ok` line and the census — which also rules
#     out the file being counted twice by the two `ls-files` streams.
if scratch_repo "$scratch"; then
  printf '#!/usr/bin/env bash\nset -eu\necho untracked and clean\n' > "$scratch/untracked-clean.sh"
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a clean UNTRACKED script passes (exit 0)" "0" "$rc"
  assert_contains "untracked: the clean untracked file is reported ok" \
    "ok  untracked-clean.sh" "$out"
  # 2 linted (tracked-clean.sh + untracked-clean.sh) of 5 discovered; the gate's
  # own copy is untracked bash under tools/ and IS linted, making 3 of 6.
  assert_contains "untracked: the census counts it exactly once" \
    "bash-lint: 3 of 6 bash file(s)" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 6c. The exclusions apply to untracked files too — a plausible
#     mis-implementation is a SECOND walk that skips the filters the first one
#     applies, which is the mistake posix-lint_test.sh's cases 10c/10d were
#     written against. Pinning the census rather than only the exit status is
#     what tells "excluded" from "walked and happened to be clean".
if scratch_repo "$scratch"; then
  printf '#!/usr/bin/env bash\nset -u\nd=""\nrm -rf "$d/lib"\n' \
    > "$scratch/tools/lib/testdata/bash-lint/untracked-bad.sh"
  printf '#!/usr/bin/env bash\nset -u\nd=""\nrm -rf "$d/lib"\n' \
    > "$scratch/replaydata/untracked-bad.sh"
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: an excluded-family untracked file is still excluded (exit 0)" "0" "$rc"
  assert_contains "untracked: the exclusion census counts them" "*/testdata/*=2" "$out"
  assert_contains "untracked: ...and the replaydata rule counts both of its own" "replaydata/*=2" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 6d. The FIRST-LINE rule. A file whose line 1 is not a bash shebang is not
#     linted, however much bash it quotes — the mirror of #1611's reason for
#     posix-lint keying on line 1, and what stops a `#!/bin/sh` script (which
#     posix-lint owns) being graded here under bash rules.
if scratch_repo "$scratch"; then
  {
    printf '#!/bin/sh\n'
    printf '# POSIX sh, and posix-lint.sh owns it. It quotes bash below.\n'
    printf 'cat > stub <<EOF\n#!/usr/bin/env bash\nd=""\nrm -rf "$d/lib"\nEOF\n'
  } > "$scratch/untracked-posix.sh"
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "first-line: a #!/bin/sh file is not linted as bash (exit 0)" "0" "$rc"
  assert_not_contains "first-line: ...and is not in the census at all" "untracked-posix.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 6e. A LOCK, not a mutation — say so rather than let it read as evidence. The
#     gate cds to its own root before discovering, so `--full-name -- :/`
#     cannot be discriminated from the bare `--others` form here today and this
#     case passes either way. It is kept because that `cd` is the only thing
#     making it true, and a refactor moving it would silently reinstate the
#     cwd-scoped walk #1609 measured.
if scratch_repo "$scratch"; then
  printf '#!/usr/bin/env bash\nset -u\nd=""\nrm -rf "$d/lib"\n' > "$scratch/untracked-bad.sh"
  out=$( cd "$scratch/tools" && ./bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked (lock): invoked from a subdirectory, still fails" "1" "$rc"
  assert_contains "untracked (lock): the root-relative path is named" \
    "FAIL untracked-bad.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# ===========================================================================
# 7. Refusal: an exclusion that matches NOTHING. This is the repo's own idiom
#    for exemption maps — `TW_EXEMPT_KEYS` in shell-lib-errexit_test.sh,
#    `nilTolerant` in construction_test.go, `shell_lib_suite_run`'s skip list —
#    keys are existence-checked, because an entry that stopped naming anything
#    real reads from the log exactly like coverage. A repo with no replaydata/
#    is that state.
# ===========================================================================
if scratch_repo "$scratch"; then
  rm -rf "$scratch/replaydata"
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "refusal: an exclusion matching nothing exits 2 (not a silent pass)" "2" "$rc"
  assert_contains "refusal: it names the dead rule" "replaydata/*" "$out"
  assert_contains "refusal: ...and repeats the reason that rule was written for" \
    "Its stated reason was" "$out"
else
  echo "  FAIL: refusal: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# ...and the same tree WITH the family present must not refuse, or the case
# above would be satisfied by a guard that fires unconditionally.
if scratch_repo "$scratch"; then
  out=$( cd "$scratch" && ./tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "refusal (vacuity): the same tree with every family present passes" "0" "$rc"
else
  echo "  FAIL: refusal: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# ===========================================================================
# 8. Refusal: an EMPTY IN-SCOPE set. Reached here in its most dangerous form —
#    a tree where bash WAS discovered and every file of it was excluded. That
#    is the shape an over-broad exclusion produces, and it must not read as a
#    clean gate. (A tree with no bash at all lands on the same refusal; this
#    variant is the one the exclusion-existence check cannot also catch.)
#
#    The gate's own copy is bash under tools/, so it is .gitignore'd rather
#    than deleted — it is the thing being run, and `--exclude-standard` is
#    exactly what makes an ignored file invisible to the walk.
# ===========================================================================
if scratch_repo "$scratch"; then
  rm -f "$scratch/tracked-clean.sh"
  printf '/tools/bash-lint.sh\n' > "$scratch/.gitignore"
  # `git rm --cached` as well as the .gitignore: scratch_repo already TRACKED
  # the gate copy, and .gitignore only governs untracked paths.
  ( cd "$scratch" && git rm -q --cached tools/bash-lint.sh && git add -A . ) >/dev/null 2>&1
  out=$( cd "$scratch" && bash tools/bash-lint.sh 2>&1 )
  rc=$?
  assert_eq "refusal: a corpus where everything was excluded exits 2" "2" "$rc"
  assert_contains "refusal: ...saying nothing was checked" "nothing was checked" "$out"
else
  echo "  FAIL: refusal: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

rm -rf "$untracked_tmp"

# ===========================================================================
# 9. Refusal: no shellcheck. A PATH without it must exit 2, not 0. `sh` and
#    `bash` stay reachable deliberately — the wrong outcome is the gate falling
#    back to something weaker (`bash -n`, say) and calling that a static check,
#    which is precisely the #1423 defect.
# ===========================================================================
stub="$(mktemp -d)"
trap 'rm -rf "${stub:?}"' EXIT
stub_path "$stub"
out=$(PATH="$stub" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "refusal: no shellcheck exits 2 (not a silent pass)" "2" "$rc"
assert_contains "refusal: names what to install" "brew install shellcheck" "$out"
# ...and with it present the SAME invocation passes, or the case above is
# satisfied by a PATH too small to run the gate at all.
stub_path "$stub" shellcheck
out=$(PATH="$stub" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "refusal (vacuity): the same stub PATH with shellcheck passes" "0" "$rc"

# ===========================================================================
# 10. A LINTER THAT CANNOT RUN MUST NOT READ AS CLEAN — #1423 reproduced inside
#     the gate built to fix it, and not hypothetical: posix-lint.sh's first
#     draft piped into `grep` and tested the capture for emptiness, so when the
#     filter failed to execute the capture came back empty, empty read as clean,
#     and it printed `ALL PASS` over an installer carrying a deliberate `[[ ]]`.
#
#     Two stubs, because there are two ways for the output to be lost and only
#     one of them has a distinctive exit code:
#       10a  exit 2 with no output — "could not check"
#       10b  exit 1 with no output — claims findings, produced none
# ===========================================================================
broken="$(mktemp -d)"
stub_path "$broken"
printf '#!/bin/sh\nexit 2\n' > "$broken/shellcheck"
chmod +x "$broken/shellcheck"
out=$(PATH="$broken" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "broken linter: silent exit-2 is a FAILURE, not a clean file" "1" "$rc"
assert_contains "broken linter: says it could not check" "could not check" "$out"

printf '#!/bin/sh\nexit 1\n' > "$broken/shellcheck"
chmod +x "$broken/shellcheck"
out=$(PATH="$broken" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "broken linter: exit-1-with-no-output is a FAILURE, not a clean file" "1" "$rc"
assert_contains "broken linter: says the output was lost" "the output was lost" "$out"
rm -rf "$broken"

# ===========================================================================
# 11. The gate lints ITSELF and its own test, under the rules it enforces. Not
#     ceremony: it is bash under tools/, so a gate that excluded its own source
#     would be the file-selection blindness this issue is about, in the one file
#     where it is least excusable.
# ===========================================================================
out=$("$LINT" "$ROOT/tools/bash-lint.sh" "$DIR/bash-lint_test.sh" 2>&1)
rc=$?
assert_eq "bootstrap: the gate and its own test pass the gate" "0" "$rc"

if [[ "$fails" -eq 0 ]]; then
  echo "$NAME: ALL PASS"
else
  echo "$NAME: $fails FAILED" >&2
  exit 1
fi
