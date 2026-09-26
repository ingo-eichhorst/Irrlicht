#!/usr/bin/env bash
# quotachiplayout-mutations_test.sh — committed mutation fixtures for the
# header quota-chip layout of issue #2063
# (platforms/macos/Irrlicht/Views/QuotaChipLayout.swift).
#
# WHY THIS FILE EXISTS. #2063 ADDS two things with no "before the fix" to run
# red: the flexible bar of the tight/dense tiers, and the rule that more than
# five providers show four chips beside the "+N more" pill.
# QuotaChipLayoutTests.testTheWorstCaseRowFitsTheHeaderBudget protects both,
# by measuring the narrowest strip each provider count can lay out and
# comparing it with the popover header's chip budget. Each mutation below
# undoes one of them and requires that test to go red with the count it
# overruns — so the fit check is shown to reach what it claims to protect,
# rather than passing because nothing could make it fail.
#
# Same mechanics and rationale as
# tools/lib/sessionlistview-resolvechipmode-mutations_test.sh (the #1995
# Swift fixture this copies): tools/mutate.sh applies and restores each
# mutation byte-for-byte, and a build break never reads as a catch. Runs on
# macOS only (test.yml's tools/lib/*_test.sh loop) — swift is not installed
# on the Linux job.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: quotachiplayout-mutations — $1 not found" >&2; exit 1; }; }
need swift
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: quotachiplayout-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "quotachiplayout-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/quotachiplayout-mutations_test.sh" >&2
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

# ── the dense tier's bar stops flexing (a fixed 60pt frame put back) ─────
assert_swift_test_goes_red \
  "fixing the dense tier's bar at 60pt makes five providers overrun the header" \
  "platforms/macos/Irrlicht/Views/QuotaChipLayout.swift" \
  $'        case .compact: return 60\n        case .tight, .dense: return nil' \
  $'        case .compact: return 60\n        case .tight: return nil\n        case .dense: return 60' \
  "QuotaChipLayoutTests/testTheWorstCaseRowFitsTheHeaderBudget" \
  '5 providers overrun the header'

# ── five chips beside the overflow pill instead of four ─────────────────────
assert_swift_test_goes_red \
  "showing five chips beside the overflow pill makes six providers overrun the header" \
  "platforms/macos/Irrlicht/Views/QuotaChipLayout.swift" \
  'static let maxBesideOverflow = 4' \
  'static let maxBesideOverflow = 5' \
  "QuotaChipLayoutTests/testTheWorstCaseRowFitsTheHeaderBudget" \
  '6 providers overrun the header'

if [[ $fails -gt 0 ]]; then
  echo "quotachiplayout-mutations: $fails FAILED"
  exit 1
fi
echo "quotachiplayout-mutations: ALL PASS"
