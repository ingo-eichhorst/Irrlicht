#!/usr/bin/env bash
# posix-lint_test.sh — unit tests for tools/posix-lint.sh. Plain bash (no
# framework), matching the style of lib/skill-lint_test.sh. Run directly, or
# via tools/preflight.sh's `tools` gate and test.yml's "Test the shared shell
# libs" step. Exits non-zero on any failed assertion.
#
# Covers issue #1423. `site/install.sh` is a POSIX sh script shipped to users
# as `curl … | sh`, where on Debian/Ubuntu `sh` is dash. The only check it had
# was `dash -n`, a PARSER check, which accepts most bashisms as ordinary
# commands: 3 of the 8 classes below, measured. So the gate that existed
# reported success without testing the property it exists to test.
#
# THE POINT OF THIS FILE is that the new gate can be shown RED. Per AGENTS.md's
# rule for gates that pass by construction, the deliberate mutation is the
# evidence — so rather than a one-off scratch mutation that disappears with the
# PR, the mutation corpus is committed under testdata/posix-lint/ and asserted
# here, one bashism class per fixture. If a future change weakens the linter,
# these go red.
#
# `good-clean.sh` carries as much weight as the eight broken fixtures: it is
# the vacuity guard. A linter that simply failed everything would satisfy all
# eight `bad-*` assertions and be worthless, and only the clean fixture can
# tell those two apart.
#
# Cases 10a-10e cover #1611, the same shape arriving through file SELECTION:
# the gate walked `git ls-files` and so could not see an untracked script,
# which since #1609 is exactly the file that puts it in scope. They build
# their own throwaway git repos, because the fixtures here are tracked and
# tracked-ness is the property under test.
#
# The last two cases assert the gate's REFUSALS rather than its findings —
# that a missing POSIX shell and a missing bashism linter each exit 2 instead
# of passing. Those are the paths by which this gate could itself become the
# thing it was built to remove: a green check that ran nothing.

set -uo pipefail   # NOT -e: assertions capture non-zero return codes

NAME="posix-lint_test"
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
LINT="$ROOT/tools/posix-lint.sh"
FIXTURES="$DIR/testdata/posix-lint"

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
    || fail "$label" "contains '$needle'" "${haystack:0:200}"
  return 0
}
assert_not_contains() {
  local label="$1" needle="$2" haystack="$3"
  [[ "$haystack" != *"$needle"* ]] && pass "$label" \
    || fail "$label" "does NOT contain '$needle'" "${haystack:0:200}"
  return 0
}

# ===========================================================================
# 0. The gate needs its two tools. Without them every assertion below would be
#    testing the refusal path instead of the detection path, and a suite that
#    quietly stopped checking detection is this issue wearing a new mask. So
#    say plainly which tools are in play, and let cases 9-10 (which REMOVE the
#    tools on purpose) be the only ones that run toolless.
# ===========================================================================
echo "== $NAME =="
have_posix_sh=0
for c in dash ash; do command -v "$c" >/dev/null 2>&1 && have_posix_sh=1 && break; done
have_linter=0
for c in checkbashisms shellcheck; do command -v "$c" >/dev/null 2>&1 && have_linter=1 && break; done

if [[ "$have_posix_sh" -ne 1 || "$have_linter" -ne 1 ]]; then
  # Not a skip-to-green. This is reported as a failure of the ENVIRONMENT so
  # it cannot be mistaken for the corpus passing. CI runs this on
  # ubuntu-latest, which ships both dash (as /bin/sh) and shellcheck 0.9.0.
  echo "  FAIL: environment — posix shell present=$have_posix_sh, bashism linter present=$have_linter" >&2
  echo "        install: brew install dash checkbashisms  |  apt-get install -y dash shellcheck" >&2
  echo "$NAME: 1 FAILED" >&2
  exit 1
fi

# ===========================================================================
# 1-8. Every bashism class must be CAUGHT. One fixture per class; the class is
#      in the filename so a red run names the construct, not just a path.
#
#      These are the mutations. On the pre-#1423 gate (`dash -n` alone) five of
#      the eight PASS — [[ ]], ${v,,}, +=, echo -e and source — which is the
#      measurement that motivated the static linter.
# ===========================================================================
for fixture in "$FIXTURES"/bad-*.sh; do
  base="$(basename "$fixture")"
  out=$("$LINT" "$fixture" 2>&1)
  rc=$?
  assert_eq "detect: $base is rejected (exit 1)" "1" "$rc"
  # Exit status alone is satisfied by the gate erroring for an unrelated
  # reason (a missing tool exits 2, but a future refactor could exit 1). Pin
  # the file's own name on a FAIL line so the rejection is about THIS file.
  assert_contains "detect: $base is named in the failure" "$base" "$out"
done

# ===========================================================================
# 9. The vacuity guard: clean POSIX sh must PASS. Without this, a linter that
#    rejected every input would satisfy all eight assertions above.
# ===========================================================================
out=$("$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "vacuity: good-clean.sh passes (exit 0)" "0" "$rc"
assert_contains "vacuity: good-clean.sh reports ALL PASS" "ALL PASS" "$out"

# 9a. The severity filter, in the only direction the bad-* fixtures cannot
#     reach. They all trip an SC3xxx, so they exercise the MATCH arm; this one
#     makes shellcheck exit 1 carrying ONLY SC2086, which the gate must
#     discard. Without it, deleting the code test (accepting every shellcheck
#     line) leaves the whole suite green while site/install.sh starts failing
#     the gate on ordinary style debt.
out=$("$LINT" "$FIXTURES/noisy-but-posix.sh" 2>&1)
rc=$?
assert_eq "filter: shellcheck-noisy but POSIX-clean still passes" "0" "$rc"

# ===========================================================================
# 9b. CI PARITY: the same corpus, through the shellcheck path specifically.
#
#     The gate runs every linter that is installed, so on this machine the
#     cases above may have been satisfied by checkbashisms. CI is not that
#     machine: the ubuntu image ships shellcheck (0.9.0) and NOT
#     checkbashisms, so shellcheck alone is what guards `main`. Pinning a
#     a shellcheck-only PATH is the only way that configuration is ever
#     exercised from a developer box that has both.
#
#     The two are genuinely not interchangeable — checkbashisms accepts
#     `local`, `set -o pipefail` and `echo -n`, all of which shellcheck
#     rejects — which is why the gate runs both rather than preferring one.
# ===========================================================================
if command -v shellcheck >/dev/null 2>&1; then
  sc_stub="$(mktemp -d)"
  for t in sh bash git awk sed basename dirname sort cat mktemp dash ash shellcheck; do
    real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$sc_stub/$t"
  done
  for fixture in "$FIXTURES"/bad-*.sh; do
    base="$(basename "$fixture")"
    out=$(PATH="$sc_stub" "$LINT" "$fixture" 2>&1)
    rc=$?
    assert_eq "shellcheck path: $base is rejected (exit 1)" "1" "$rc"
  done
  out=$(PATH="$sc_stub" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
  rc=$?
  assert_eq "shellcheck path: good-clean.sh still passes" "0" "$rc"
  assert_contains "shellcheck path: reports which linter ran" "bashisms=shellcheck" "$out"
  # The filter, exercised through the branch that actually guards main.
  out=$(PATH="$sc_stub" "$LINT" "$FIXTURES/noisy-but-posix.sh" 2>&1)
  rc=$?
  assert_eq "shellcheck path: SC2086-only noise is not a bashism" "0" "$rc"
  rm -rf "$sc_stub"
else
  echo "  FAIL: shellcheck absent — the branch that guards CI cannot be tested here" >&2
  fails=$((fails + 1))
fi

# ===========================================================================
# 9c. A LINTER THAT CANNOT RUN MUST NOT READ AS CLEAN.
#
#     This is #1423 reproduced inside the gate built to fix it, and it is not
#     hypothetical: the first draft piped the linter into `grep` and tested
#     the captured string for emptiness, so when the filter failed to execute
#     the capture came back empty, empty read as clean, and the gate printed
#     `ALL PASS` over an installer carrying a deliberate `[[ ]]`.
#
#     The stub here is a linter that exits 2 (the "could not check" status)
#     while printing nothing at all — the exact shape that a naive
#     empty-output test waves through.
# ===========================================================================
broken="$(mktemp -d)"
for t in sh bash git awk sed basename dirname sort cat mktemp dash ash; do
  real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$broken/$t"
done
printf '#!/bin/sh\nexit 2\n' > "$broken/shellcheck"
chmod +x "$broken/shellcheck"
out=$(PATH="$broken" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "broken linter: silent exit-2 is a FAILURE, not a clean file" "1" "$rc"
assert_contains "broken linter: says it could not check" "could not check" "$out"
rm -rf "$broken"

# ===========================================================================
# 10. Discovery. The gate must find the real installer and must NOT walk its
#     own deliberately-broken corpus — if it did, CI would be permanently red
#     and the corpus would have to be deleted, taking the evidence with it.
# ===========================================================================
out=$("$LINT" 2>&1)
rc=$?
assert_eq "discovery: the repo's real #!/bin/sh scripts pass (exit 0)" "0" "$rc"
assert_contains "discovery: site/install.sh is in scope" "site/install.sh" "$out"
assert_not_contains "discovery: testdata fixtures are excluded" "testdata/posix-lint" "$out"

# ===========================================================================
# 10a-10e. UNTRACKED FILES (#1611).
#
#     `git ls-files` lists index entries only. That was harmless until #1609
#     taught `changed_files_vs_origin_main` to include untracked files: a new,
#     untracked `#!/bin/sh` script now DOES put this gate in scope via
#     `tools/preflight.sh --changed`, and the gate then walked the index and
#     could not see the file that summoned it — `ALL PASS` over a file it
#     never read. That is #1423 arriving through file selection instead of
#     through the pipeline, so it gets the same treatment: a committed
#     mutation rather than one that disappears with the PR.
#
#     These cases cannot use the fixtures above — those are TRACKED, which is
#     the one property under test — so each builds a throwaway git repo and
#     plants its own untracked file there.
#
#     Measured on the pre-fix gate, the same scratch repo: `posix-lint: 1
#     file(s)` / `ALL PASS` / exit 0 with `untracked-bad.sh` sitting in the
#     tree carrying a deliberate `[[ ]]`.
# ===========================================================================

# scratch_repo <dir> — a throwaway git repo holding a copy of the gate at the
# path it derives its root from (<root>/tools/posix-lint.sh, via $0) plus ONE
# tracked, clean `#!/bin/sh` script. Callers plant the untracked file whose
# treatment is under test, so the file-count line below is a full census.
#
# The gate copy is itself untracked and carries a `#!/usr/bin/env bash`
# shebang, so it doubles as "an untracked file that is NOT POSIX sh must not
# be dragged into the walk" — if it were, every count below would be one high.
scratch_repo() {
  local d="$1"
  rm -rf "$d" && mkdir -p "$d/tools" && cp "$LINT" "$d/tools/posix-lint.sh" || return 1
  (
    cd "$d" || exit 1
    git init -q . || exit 1
    printf '#!/bin/sh\necho clean\n' > tracked-clean.sh || exit 1
    git add tracked-clean.sh || exit 1
    # The fixture IS the test here. A scratch repo that did not come up would
    # make every assertion below grade a tree git cannot read, and a gate that
    # found nothing to lint is indistinguishable from a gate that found
    # nothing wrong — the shape this whole file exists to refuse.
    [[ -n "$(git ls-files)" ]] || exit 1
  ) || return 1
}

untracked_tmp="$(mktemp -d)"
scratch="$untracked_tmp/repo"

# 10a. THE MUTATION. An untracked #!/bin/sh file carrying a bashism must make
#      the gate FAIL and must be NAMED. Without this case the fix is
#      unfalsifiable: the gate passes on a clean tree either way, and passing
#      is exactly what it did before for the wrong reason.
if scratch_repo "$scratch"; then
  printf '#!/bin/sh\nif [[ "$1" == x ]]; then echo hi; fi\n' > "$scratch/untracked-bad.sh"
  out=$( cd "$scratch" && ./tools/posix-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a bashism in an UNTRACKED script fails the gate" "1" "$rc"
  assert_contains "untracked: the untracked file is named on a FAIL line" \
    "FAIL [bashisms] untracked-bad.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 10b. The direction that fails SILENTLY, and therefore the one that needs the
#      stronger assertion: a clean untracked script must pass AND must have
#      been SEEN. Exit 0 alone is satisfied by never looking at it, which is
#      the defect. So pin the census — 2 files, the tracked one and the
#      untracked one, which also rules out the file being counted twice — and
#      pin the per-file `ok` line that says the gate actually read it.
if scratch_repo "$scratch"; then
  printf '#!/bin/sh\necho untracked and clean\n' > "$scratch/untracked-clean.sh"
  out=$( cd "$scratch" && ./tools/posix-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a clean UNTRACKED script passes (exit 0)" "0" "$rc"
  assert_contains "untracked: the file count moved — it was actually walked" \
    "posix-lint: 2 file(s)" "$out"
  assert_contains "untracked: the clean untracked file is reported ok" \
    "ok  untracked-clean.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 10c. The testdata/ exclusion applies to untracked files too — a plausible
#      mis-implementation is a SECOND walk that skips the filters the first
#      one applies. Pinning the count at 1 rather than only the exit status is
#      what tells "excluded" from "walked and happened to be clean".
if scratch_repo "$scratch"; then
  mkdir -p "$scratch/tools/lib/testdata/posix-lint"
  printf '#!/bin/sh\nif [[ "$1" == x ]]; then echo hi; fi\n' \
    > "$scratch/tools/lib/testdata/posix-lint/bad-untracked.sh"
  out=$( cd "$scratch" && ./tools/posix-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a fixture under testdata/ is still excluded (exit 0)" "0" "$rc"
  assert_contains "untracked: testdata/ exclusion leaves the census at 1" \
    "posix-lint: 1 file(s)" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 10d. The FIRST-LINE rule applies to untracked files too. This file is bash
#      and writes a `#!/bin/sh` stub inside a heredoc — exactly the shape of
#      tools/lib/install-uninstall_test.sh, which is why the selector reads
#      line 1 and not the file's content.
if scratch_repo "$scratch"; then
  {
    printf '#!/usr/bin/env bash\n'
    printf 'cat > stub <<EOF\n#!/bin/sh\nif [[ "$1" == x ]]; then echo hi; fi\nEOF\n'
  } > "$scratch/untracked-bash.sh"
  out=$( cd "$scratch" && ./tools/posix-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked: a bash file quoting a #!/bin/sh stub is not linted (exit 0)" "0" "$rc"
  assert_contains "untracked: first-line rule leaves the census at 1" \
    "posix-lint: 1 file(s)" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

# 10e. A LOCK, not a mutation — say so rather than let it read as evidence.
#      #1609 measured that a bare `git ls-files --others` lists only what is
#      under the CALLER'S cwd, and prints it cwd-relative, so the naive
#      spelling looks correct forever from the repo root. The gate cds to its
#      own root before discovering, so `--full-name -- :/` cannot be
#      discriminated from the bare form here today and this case passes either
#      way. It is kept because the `cd` is the only thing making that true,
#      and a future refactor that moves it would otherwise silently reinstate
#      the cwd-scoped walk.
if scratch_repo "$scratch"; then
  printf '#!/bin/sh\nif [[ "$1" == x ]]; then echo hi; fi\n' > "$scratch/untracked-bad.sh"
  out=$( cd "$scratch/tools" && ./posix-lint.sh 2>&1 )
  rc=$?
  assert_eq "untracked (lock): invoked from a subdirectory, still fails" "1" "$rc"
  assert_contains "untracked (lock): the root-relative path is named" \
    "FAIL [bashisms] untracked-bad.sh" "$out"
else
  echo "  FAIL: untracked: scratch repo could not be built" >&2
  fails=$((fails + 1))
fi

rm -rf "$untracked_tmp"

# ===========================================================================
# 11. Refusal: no POSIX shell. A PATH without dash/ash must exit 2, not 0.
#     `sh` is deliberately still reachable — the wrong outcome here is the gate
#     falling back to bash-as-sh and calling that a POSIX check, which is
#     precisely the #1423 defect.
# ===========================================================================
stub="$(mktemp -d)"
trap 'rm -rf "$stub"' EXIT
for t in sh bash git grep awk sed basename dirname sort cat mktemp; do
  real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$stub/$t"
done
# checkbashisms/shellcheck are linked in so this case isolates the SHELL.
for t in checkbashisms shellcheck; do
  real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$stub/$t"
done
out=$(PATH="$stub" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "refusal: no dash/ash exits 2 (not a silent pass)" "2" "$rc"
assert_contains "refusal: says which shells it looked for" "dash" "$out"

# ===========================================================================
# 12. Refusal: no bashism linter. A PATH with a POSIX shell but no
#     checkbashisms/shellcheck must exit 2. This is the one that matters most:
#     `dash -n` alone SUCCEEDS on five of the eight fixtures, so a gate that
#     degraded to parser-only here would go green while missing most bashisms.
# ===========================================================================
rm -f "$stub/checkbashisms" "$stub/shellcheck"
for t in dash ash; do
  real="$(command -v "$t" 2>/dev/null)" && ln -sf "$real" "$stub/$t"
done
out=$(PATH="$stub" "$LINT" "$FIXTURES/good-clean.sh" 2>&1)
rc=$?
assert_eq "refusal: no bashism linter exits 2 (not parser-only green)" "2" "$rc"
assert_contains "refusal: names an installable linter" "checkbashisms" "$out"

if [ "$fails" -eq 0 ]; then
  echo "$NAME: ALL PASS"
else
  echo "$NAME: $fails FAILED" >&2
  exit 1
fi
