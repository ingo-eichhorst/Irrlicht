#!/usr/bin/env bash
# pi-provider-absent-not-confirmed-mutations_test.sh — the committed mutation
# fixture for issue #2005's "absent is not empty-confirmed" guard in Pi's
# model_change handler (core/adapters/inbound/agents/pi/parser.go).
#
# WHY THIS FILE EXISTS. #2005 adds classifyPiProviderRoute, which classifies
# a model_change event's "provider" field into one of five states before
# applyPiModelChangeProvider decides whether to stamp ev.RateLimit. The rule
# that an ABSENT provider must stay unknown — never publish an empty-but-
# "confirmed" attribution — has no "before the fix" to run red: before #2005
# there was no provider-reading code at all, so there is nothing to mutate
# back to. Per AGENTS.md and docs/testing-philosophy.md, a guard like this
# earns its place only by being seen to fail when broken, not by having been
# red before it existed.
#
# The mutation flips classifyPiProviderRoute's absent-case return from
# piProviderRouteAbsent to piProviderRouteMapped (with the same "" provider
# value it already returns for that branch). That is exactly the defect this
# guard prevents: an absent "provider" key would then satisfy
# applyPiModelChangeProvider's `state == piProviderRouteMapped` check and
# stamp ev.RateLimit with Provider="" at AttributionQuality=confirmed —
# an empty CONFIRMED attribution — instead of leaving ev.RateLimit nil.
# TestParser_ModelChange_NoProviderKeepsModel asserts ev.RateLimit stays nil
# for exactly this input, so it must go red under the mutation.
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
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: pi-provider-absent-not-confirmed-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: pi-provider-absent-not-confirmed-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "pi-provider-absent-not-confirmed-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/pi-provider-absent-not-confirmed-mutations_test.sh" >&2
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

# ── The "absent is not empty-confirmed" guard is broken ─────────────────────
#
# Flipping classifyPiProviderRoute's absent-case return to
# piProviderRouteMapped makes applyPiModelChangeProvider treat an absent
# "provider" key as a resolved (empty) provider, stamping ev.RateLimit with
# Provider="" at AttributionQuality=confirmed instead of leaving it nil.
assert_go_test_goes_red \
  "flipping the absent-provider classification to mapped publishes an empty confirmed attribution" \
  "core/adapters/inbound/agents/pi/parser.go" \
  $'\t\treturn piProviderRouteAbsent, ""' \
  $'\t\treturn piProviderRouteMapped, ""' \
  "./core/adapters/inbound/agents/pi/..." \
  'TestParser_ModelChange_NoProviderKeepsModel' \
  "expected no RateLimit when provider is absent"

if [[ $fails -gt 0 ]]; then
  echo "pi-provider-absent-not-confirmed-mutations: $fails FAILED"
  exit 1
fi
echo "pi-provider-absent-not-confirmed-mutations: ALL PASS"
