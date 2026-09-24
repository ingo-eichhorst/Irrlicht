#!/usr/bin/env bash
# tailer-provider-constants-mutations_test.sh — the committed mutation
# fixture for #1994's TestRateLimitProviderConstantsAgree
# (core/pkg/tailer/rate_limit_provider_contract_test.go).
#
# WHY THIS FILE EXISTS. core/pkg/tailer/parser.go deliberately duplicates
# core/domain/session/rate_limit.go's ProviderAnthropic, ProviderOpenAI, and
# AttributionQualityConfirmed constants rather than importing the domain
# (RateLimitSnapshot's own doc comment: parsers emit it without pulling the
# domain in). TestRateLimitProviderConstantsAgree pins the two copies
# against each other — the same shape as this package's existing
# TestUserBlockingListsAgree for isUserBlockingToolName. That pin is a check
# #1994 ADDS: it has no "before the fix" to run red (before this ticket
# there was no tailer-side copy of these constants at all), so per
# AGENTS.md and docs/testing-philosophy.md it earns its place by being seen
# to fail when the thing it protects is broken, not by red-first evidence.
#
# This script drives tools/mutate.sh to change ProviderOpenAI's tailer-side
# copy to a wrong value and requires TestRateLimitProviderConstantsAgree to
# go red under it — proving the contract test actually reaches the
# duplication it claims to guard, not just that it compiles.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards (a mutation that matched zero or more than
# one site fails loudly rather than silently doing nothing or the wrong
# thing), and the byte-for-byte restore that never touches git state
# (worktrees share the parent repo's .git dir, so `git checkout --` /
# `git restore` / `git reset --hard` are banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: tailer-provider-constants-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: tailer-provider-constants-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "tailer-provider-constants-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/tailer-provider-constants-mutations_test.sh" >&2
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
# Applies one mutation and requires the named Go test to FAIL under it, AND
# to fail for the right reason. Both halves matter: a mutation that leaves
# the test green means the contract does not reach what it claims to
# protect, and a mutation that goes red because the package no longer
# COMPILES would otherwise read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -count=1 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE anchor means the surrounding text"
    echo "      moved and this fixture needs its anchor updated; it does NOT mean the guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the contract does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the contract. A fixture that"
    echo "      cannot compile proves nothing about the assertion it is meant to exercise."
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

# ── The tailer-side ProviderOpenAI copy drifts from the domain original ────
assert_go_test_goes_red \
  "a wrong tailer-side ProviderOpenAI copy reddens the contract test" \
  "core/pkg/tailer/parser.go" \
  $'\tProviderOpenAI    = "openai"' \
  $'\tProviderOpenAI    = "wrong-value"' \
  "./core/pkg/tailer/..." \
  "TestRateLimitProviderConstantsAgree" \
  'tailer copy "wrong-value" disagrees with session.ProviderOpenAI "openai"'

if [[ $fails -gt 0 ]]; then
  echo "tailer-provider-constants-mutations: $fails FAILED"
  exit 1
fi
echo "tailer-provider-constants-mutations: ALL PASS"
