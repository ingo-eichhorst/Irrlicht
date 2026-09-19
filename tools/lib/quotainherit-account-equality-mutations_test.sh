#!/usr/bin/env bash
# quotainherit-account-equality-mutations_test.sh — the committed mutation
# fixture for issue #1994's account-equality guard in applyDonors
# (core/application/services/quotainherit.go).
#
# WHY THIS FILE EXISTS. #1994 removed the Anthropic rate-limit "singleton"
# (an empty AccountID that used to match any same-provider wrapper) and, in
# doing so, turned the account-equality check that USED to be implicit in Go's
# own map-key equality (`donors[key]`, where AccountKey was the map key) into
# an explicit, single comparison in applyDonors:
#
#     if d.accountID != key.AccountID {
#             continue
#     }
#
# That guard is a check #1994 ADDS — there is no "before the fix" version of
# it to run red, since before #1994 there was no dedicated line to delete in
# the first place (the equality lived inside the map lookup). Per AGENTS.md
# and docs/testing-philosophy.md, a guard like this earns its place only by
# being seen to fail when deleted, not by having been red before it existed.
#
# Deleting it makes applyDonors hand a recipient the FIRST donor snapshot for
# its provider regardless of account, which is exactly the defect
# TestInheritRateLimits_PiNotInheritsOnAccountMismatch exists to catch (a pi
# session configured for one OpenAI account must not inherit a Codex
# session's snapshot for a DIFFERENT OpenAI account). It must go red under
# the mutation. TestInheritRateLimits_PiOpenAIInheritsFromCodex — the
# matching-account case — is asserted to stay green in the SAME run, so the
# mutation is shown to break account discrimination specifically, not the
# donor path in general (a mutation that broke everything would "pass" this
# fixture's red-test check for the wrong reason).
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale-anchor guard (refuses if the anchor text no longer occurs, so a
# no-op mutation — e.g. the guard already moved or was rewritten — fails
# loudly instead of silently reporting a clean pass), the ambiguous-anchor
# guard (refuses unless the anchor occurs EXACTLY ONCE), and the byte-for-byte
# restore that never touches git state (worktrees share the parent repo's
# .git dir, so `git checkout --` / `git restore` / `git reset --hard` are
# banned repo-wide).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: quotainherit-account-equality-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: quotainherit-account-equality-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "quotainherit-account-equality-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/quotainherit-account-equality-mutations_test.sh" >&2
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
# Applies one mutation and requires the named Go tests to FAIL under it, AND
# to fail for the right reason. Both halves matter: a mutation that leaves
# the tests green means the guard does not reach what it claims to protect,
# and a mutation that goes red because the package no longer COMPILES would
# otherwise read as success.
assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the tests stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  # Red for the RIGHT reason: an actual assertion failure naming the expected
  # text, not a build error.
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
    echo "      cannot compile proves nothing about the assertion it is meant to exercise."
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

# ── The account-equality guard is deleted ───────────────────────────────────
#
# Deleting the `if d.accountID != key.AccountID { continue }` guard makes the
# donor loop hand back the first donor for the recipient's PROVIDER,
# regardless of account: a mismatched-account pi session wrongly inherits a
# codex session's snapshot for a different account
# (TestInheritRateLimits_PiNotInheritsOnAccountMismatch goes red), while a
# matching-account pi session is unaffected either way
# (TestInheritRateLimits_PiOpenAIInheritsFromCodex stays green under the same
# mutation, since there both. mutated and unmutated donor is the correct one).
assert_go_test_goes_red \
  "deleting the account-equality guard lets a mismatched account inherit" \
  "core/application/services/quotainherit.go" \
  $'\t\t\tif d.accountID != key.AccountID {\n\t\t\t\tcontinue\n\t\t\t}' \
  '' \
  "./core/application/services/..." \
  'TestInheritRateLimits_PiNotInheritsOnAccountMismatch' \
  "expected no inheritance when account ids don't match"

if [[ $fails -gt 0 ]]; then
  echo "quotainherit-account-equality-mutations: $fails FAILED"
  exit 1
fi
echo "quotainherit-account-equality-mutations: ALL PASS"
