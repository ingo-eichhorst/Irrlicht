#!/usr/bin/env bash
# execution-confidence-swift-mutations_test.sh — the committed mutation
# fixtures for #737 Phase 3, the macOS half of the low execution-confidence
# chip (ExecutionConfidenceChip in platforms/macos/Irrlicht/Views/
# SessionRowView.swift, the three SessionMetrics fields in
# platforms/macos/Irrlicht/Models/SessionState.swift, tests in
# platforms/macos/Tests/ExecutionConfidenceTests.swift).
#
# WHY THIS FILE EXISTS. The chip's gate is something #737 ADDS, so it has no
# "before the fix" to run red; per AGENTS.md and docs/testing-philosophy.md
# each rule earns its place by being seen to fail when what it protects is
# broken. The three rules, each mutated below:
#   1. the chip shows ONLY on the daemon's execution_confidence_low == true
#      (the client never applies a threshold of its own);
#   2. the gate is that flag, never the score's truthiness — a score of 0 is
#      the least confident a session can be and must show;
#   3. the Swift CodingKeys are the daemon's json tags, so a renamed key
#      decodes nothing silently.
# Each was run by hand with these exact anchors before this file was
# written, and went red with the messages asserted below. The Swift-side
# twin of tools/lib/execution-confidence-mutations_test.sh (Phase 1, Go).
#
# Structure, the dirty-tree rule and the build-break check are copied from
# tools/lib/sessionlistview-resolvechipmode-mutations_test.sh, which explains
# why a compiled XCTest assertion is driven through tools/mutate.sh directly
# rather than through tools/lib/mutation-assert.sh. Runs on macos-latest only
# (test.yml's tools/lib/*_test.sh loop) — `swift` is not installed on the
# Linux job.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: execution-confidence-swift-mutations — $1 not found" >&2; exit 1; }; }
need swift
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: execution-confidence-swift-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses (exit 4) against an already-dirty tree, because its
# post-restore emptiness check could prove nothing then.
#
# A DIRTY TREE MUST NOT SILENTLY PASS: this suite's runner (shell-lib-suite.sh)
# judges a script by its EXIT STATUS and has no self-skip protocol, so an
# `exit 0` here would make "the guard was verified" and "the guard could not
# be checked at all" produce byte-identical results at the gate. So it is a
# HARD FAILURE wherever the answer is load-bearing (CI, and any caller that
# sets MUTATION_FIXTURES_STRICT=1), and a loud, non-silent skip on a
# developer's dirty worktree, where failing would only train people to delete
# the fixture.
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "execution-confidence-swift-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/execution-confidence-swift-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running these mutations)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

# assert_swift_test_goes_red <label> <file> <anchor> <replacement> <test-id> <want-in-output>
#
# Applies one mutation and requires the named Swift test (a full
# <TestClass>/<testMethod> id, as `swift test --filter` takes) to FAIL under
# it, and to fail for the right reason — a build break must not read as
# success, the same distinction assert_go_test_goes_red draws for `go test`.
assert_swift_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" test_id="$5" want="$6"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "cd platforms/macos && swift test --filter '$test_id' 2>&1; echo SWIFT_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'SWIFT_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so it does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  # A build failure must not read as the test catching the mutation: no
  # "Executed N tests" summary line means the bundle never ran at all.
  if ! grep -q 'Executed [0-9]* tests\?,' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the assertion (no \"Executed N"
    echo "      tests\" summary appeared). A fixture that cannot compile proves nothing about the"
    echo "      behavior it is meant to exercise."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

GATE=$'        guard let metrics, metrics.executionConfidenceLow == true else { return nil }'

# ── 1. drop the low-flag gate ───────────────────────────────────────────────
assert_swift_test_goes_red \
  "dropping the execution_confidence_low gate shows the chip for a non-low session" \
  "platforms/macos/Irrlicht/Views/SessionRowView.swift" \
  "$GATE" \
  $'        guard let metrics else { return nil }' \
  "ExecutionConfidenceTests/testHiddenWhenTheLowFlagIsExplicitlyFalse" \
  'XCTAssertNil failed: "ExecutionConfidenceChip(text: "? 10", tooltip: "t")"'

# ── 2. gate on the score's truthiness as well ───────────────────────────────
assert_swift_test_goes_red \
  "also requiring a non-zero score hides the chip for a low session scored 0" \
  "platforms/macos/Irrlicht/Views/SessionRowView.swift" \
  "$GATE" \
  $'        guard let metrics, metrics.executionConfidenceLow == true, (metrics.executionConfidence ?? 0) != 0 else { return nil }' \
  "ExecutionConfidenceTests/testShownWhenLowWithScoreZero" \
  'XCTAssertEqual failed: ("nil") is not equal to ("Optional("? 0")")'

# ── 3. a CodingKeys raw value drifts from the daemon's json tag ─────────────
assert_swift_test_goes_red \
  "misspelling the execution_confidence_low CodingKey is caught against metrics.go" \
  "platforms/macos/Irrlicht/Models/SessionState.swift" \
  $'        case executionConfidenceLow = "execution_confidence_low"' \
  $'        case executionConfidenceLow = "execution_confidence_is_low"' \
  "ExecutionConfidenceTests/testCodingKeysAreTheDaemonsJSONTags" \
  'metrics.go has no json tag execution_confidence_is_low'

if [[ $fails -gt 0 ]]; then
  echo "execution-confidence-swift-mutations: $fails FAILED"
  exit 1
fi
echo "execution-confidence-swift-mutations: ALL PASS"
