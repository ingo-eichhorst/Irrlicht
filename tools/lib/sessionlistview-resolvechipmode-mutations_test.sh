#!/usr/bin/env bash
# sessionlistview-resolvechipmode-mutations_test.sh — the committed mutation
# fixture for QuotaChipModeTests.testAutoPrefersSubscriptionWhenWindowsArePresentEvenWithCredits
# (platforms/macos/Tests/QuotaChipModeTests.swift), issue #1995.
#
# WHY THIS FILE EXISTS. SessionListView.resolveChipMode's auto-detection rule
# changed: for a snapshot carrying BOTH windows and a credits balance with no
# plan_type, it used to resolve `.usage` (keyed on planType being empty) and
# now resolves `.subscription` (keyed on windows being present) — realigning
# it with quotaChips.js's chipModeFor, which the two clients had silently
# diverged on. This is exactly the shape docs/testing-philosophy.md and
# AGENTS.md ask for a mutation on rather than red-first evidence: the
# behavior was deliberately CHANGED going forward, not a pre-existing defect
# whose "before" state is worth reproducing — the value of the test is that
# reverting the rule reddens it, not that some past daemon output triggered
# it. Run by hand during that change (mutate the auto branch back to the old
# `(snap.credits != nil && (snap.planType ?? "").isEmpty) ? .usage :
# .subscription`, confirm exactly the one test reddens, revert); this file
# is that same perturbation, committed so it re-runs rather than living only
# in a commit message (AGENTS.md: "prefer committing that mutation to
# describing it").
#
# NOT routed through tools/lib/mutation-assert.sh's shared assert_mutation_is_red,
# unlike the newer #1823 fixtures. That helper's LOCK_TEST contract is a bash
# script that prints `^FAIL: ` lines — built for wrapping a shell-level lint
# or text guard. The thing being protected here is a compiled XCTest
# assertion, not text in a tracked file; wrapping `swift test --filter` in a
# throwaway script only to satisfy that contract would add a second
# `tools/lib/*_test.sh` entry whose only job is unwrapping one filtered
# test, which is more indirection than the two Go-based #1994 fixtures below
# need for the equivalent case (`go test -run <regex>` IS their test-cmd,
# no wrapper required). This file follows those two directly instead:
# tools/lib/tailer-provider-constants-mutations_test.sh and
# tools/lib/quotainherit-account-equality-mutations_test.sh.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards (a mutation that matched zero or more than
# one site fails loudly rather than silently doing nothing or the wrong
# thing), and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).
#
# This runs on macos-latest only (test.yml's tools/lib/*_test.sh loop), never
# on the ubuntu Linux job — `swift` is not installed there. See that
# workflow's own comments: the loop is deliberately absent from linux.yml,
# which runs only posix-lint_test.sh/bash-lint_test.sh directly (the two
# that need ubuntu's shellcheck).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: sessionlistview-resolvechipmode-mutations — $1 not found" >&2; exit 1; }; }
need swift
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: sessionlistview-resolvechipmode-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "sessionlistview-resolvechipmode-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/sessionlistview-resolvechipmode-mutations_test.sh" >&2
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

# ── resolveChipMode's auto branch reverts to the old planType-keyed rule ───
assert_swift_test_goes_red \
  "reverting resolveChipMode's auto branch to the old planType-keyed rule reddens the harmonization test" \
  "platforms/macos/Irrlicht/Views/SessionListView.swift" \
  $'        case .auto:\n            if !snap.windows.isEmpty { return .subscription }\n            if snap.credits != nil { return .usage }\n            return .subscription\n        }' \
  $'        case .auto:\n            return (snap.credits != nil && (snap.planType ?? "").isEmpty)\n                ? .usage\n                : .subscription\n        }' \
  "QuotaChipModeTests/testAutoPrefersSubscriptionWhenWindowsArePresentEvenWithCredits" \
  'XCTAssertEqual failed: ("usage") is not equal to ("subscription")'

if [[ $fails -gt 0 ]]; then
  echo "sessionlistview-resolvechipmode-mutations: $fails FAILED"
  exit 1
fi
echo "sessionlistview-resolvechipmode-mutations: ALL PASS"
