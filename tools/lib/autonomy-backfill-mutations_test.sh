#!/usr/bin/env bash
# autonomy-backfill-mutations_test.sh — the committed mutation fixtures for the
# Autonomy back-fill's new checks (#1905 back-fill).
#
# WHY THIS FILE EXISTS. Everything the back-fill adds — the `source` marking,
# the restart-straddle drop, the orphan-close drop, and the `unknown` end
# reason for the cost era — is a check with no "before the fix" to run red. Per
# AGENTS.md and docs/testing-philosophy.md, such a check earns its place only
# by being seen to fail when the thing it protects is broken.
#
# Five independent breakages, proven separately, because one combined mutation
# could pass while any single one of them was actually unguarded:
#
#   1. THE MARKING GOES AWAY. session.IsAutonomyReconstructed always answers
#      "measured". Every reconstructed row then reads back as live, both
#      clients fall silent, and four months of rebuilt history is presented as
#      measurement — the exact failure the marking exists to prevent.
#
#   2. A RESTART-STRADDLING SPAN IS KEPT. straddlesRestart always answers no.
#      Several runs merged across a daemon restart — with the transitions
#      between them never logged — then ship as ONE long autonomous run, which
#      is the single largest fabricated number this reconstruction could
#      produce.
#
#   3. A CLOSE WITH NO OPEN INVENTS A SPAN. The orphan branch emits instead of
#      counting. No start means no duration; making one up manufactures runs
#      out of the log's own truncation boundary.
#
#   4. THE COST ERA GUESSES ITS END REASON. `unknown` becomes `ready`. This is
#      the mutation the whole honesty argument is about: it is invisible on the
#      duration chart (which never reads the reason) and paints months of the
#      strip green under a "the turn finished" claim nobody measured.
#
#   5. A REBUILT RUN LANDS ON TOP OF A MEASURED ONE. The live-floor bound stops
#      bounding. The daemon's event log keeps being written after the feature
#      ships, so its era overlaps the live span log's — and a run present in
#      both is counted twice, which is the one error a back-fill can make that
#      looks like productivity rather than like a bug.
#
# The panel-provenance check — "state it when any span in view is
# reconstructed, say nothing when none is" — is proven by COMMITTED IN-LANGUAGE
# MUTANTS instead, in platforms/web/irrlicht.history.autonomy.backfill.test.js
# and platforms/macos/Tests/HistoryAutonomyBackfillTests.swift, following the
# idiom those suites already use (`mergedResolver`, `denseBuckets`). Driving
# vitest and `swift test` from here would make this fixture depend on an
# installed node_modules and a multi-minute Swift build, and a fixture that
# cannot run is worse than one scoped to what it can.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale-anchor guard, the no-op replacement refusal, and the byte-for-byte
# restore that never touches git state (worktrees share the parent repo's .git
# dir, so `git checkout --` / `git restore` / `git reset --hard` are banned
# repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as a
# PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: autonomy-backfill-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: autonomy-backfill-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS: this suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guards were verified" and "the guards could not
# be checked at all" produce byte-identical results at the gate. So it is a
# HARD FAILURE wherever the answer is load-bearing (CI, and any caller that
# sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "autonomy-backfill-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/autonomy-backfill-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_go_test_goes_red <label> <file> <anchor> <replacement> <pkg> <run-regex> <want-in-output>
#
# Applies one mutation and requires the named Go tests to FAIL under it, AND to
# fail for the right reason. Both halves matter: a mutation that leaves the
# tests green means the check does not reach what it claims to cover, and a
# mutation that goes red because the package no longer COMPILES would otherwise
# read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the check is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the tests stayed GREEN under the mutation, so they do not reach what they"
    echo "      claim to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  # Red for the RIGHT reason: an actual assertion failure naming the expected
  # text, not a build error.
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the check. A fixture that cannot"
    echo "      compile proves nothing about the assertion it is meant to exercise."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF "$want" <<<"$out"; then
    echo "FAIL: $label — the tests failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

# ── 1. The marking goes away ────────────────────────────────────────────────
assert_go_test_goes_red \
  "a reconstructed span still reads back as reconstructed" \
  "core/domain/session/autonomy.go" \
  'func IsAutonomyReconstructed(source string) bool { return source != "" }' \
  'func IsAutonomyReconstructed(source string) bool { return false }' \
  "./core/adapters/outbound/filesystem/..." \
  "TestAutonomySpanTracker_MarksReconstructedRowsAndLeavesLiveOnesBare|TestAutonomySpanTracker_UnknownSourceStillCountsAsReconstructed" \
  "want 2"

# ── 2. A restart-straddling span is kept ────────────────────────────────────
assert_go_test_goes_red \
  "a span crossing a daemon restart is dropped" \
  "tools/autonomy-backfill/reconstruct.go" \
  $'\ti := sort.Search(len(restarts), func(i int) bool { return restarts[i] > s.Start })\n\treturn i < len(restarts) && restarts[i] < s.End' \
  $'\t_ = restarts\n\treturn false' \
  "./tools/autonomy-backfill/..." \
  "TestRestartStraddlingSpanIsDropped" \
  "kept 2 spans, want 1"

# ── 3. A close with no open invents a span ──────────────────────────────────
assert_go_test_goes_red \
  "a close with no matching open emits nothing" \
  "tools/autonomy-backfill/reconstruct.go" \
  $'\tb.loss.OrphanCloses++\n}' \
  $'\tb.emit(t.TS-1, t.TS, t.Session, t.State)\n\tb.loss.OrphanCloses++\n}' \
  "./tools/autonomy-backfill/..." \
  "TestSpanRules" \
  "want []"

# ── 4. The cost era guesses its end reason ──────────────────────────────────
assert_go_test_goes_red \
  'a cost-derived span carries `unknown` and never a guessed reason' \
  "tools/autonomy-backfill/reconstruct.go" \
  $'\t\t\t\t\tReason:  session.AutonomyReasonUnknown,\n\t\t\t\t\tSource:  session.AutonomySourceCost,' \
  $'\t\t\t\t\tReason:  session.StateReady,\n\t\t\t\t\tSource:  session.AutonomySourceCost,' \
  "./tools/autonomy-backfill/..." \
  "TestCostSpansCarryUnknownAndNeverAGuessedReason" \
  "want \"unknown\""

# ── 5. A reconstructed run is written on top of a measured one ──────────────
assert_go_test_goes_red \
  "a span reaching into the era the daemon already measured is dropped" \
  "tools/autonomy-backfill/reconstruct.go" \
  $'\tif liveFloor <= 0 {\n\t\treturn spans, 0\n\t}' \
  $'\tif liveFloor >= 0 {\n\t\treturn spans, 0\n\t}' \
  "./tools/autonomy-backfill/..." \
  "TestSpansReachingIntoTheMeasuredEraAreDropped|TestApplyNeverWritesIntoTheMeasuredEra" \
  "reaches into the measured era"

if [[ $fails -gt 0 ]]; then
  echo "autonomy-backfill-mutations: $fails FAILED"
  exit 1
fi
echo "autonomy-backfill-mutations: ALL PASS"
