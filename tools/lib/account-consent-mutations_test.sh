#!/usr/bin/env bash
# account-consent-mutations_test.sh — the committed mutation fixtures for
# issue #2095's consent gates on account reads:
#   1. the Claude Code account read
#      (core/adapters/inbound/agents/claudecode/account.go),
#   2. the Codex account read (core/adapters/inbound/agents/codex/account.go),
#   3. the Pi/OpenCode recipient auth.json read of the rate-limit inheritance
#      pass (core/application/services/quotainherit.go, adapterGranted).
#
# The rest of this header describes (1); (2) and (3) are the same shape —
# one grant line each, deleted, with the named gate tests required to go red.
#
# WHY THIS FILE EXISTS. #2095 adds a permission ("claude-code-account" /
# "account") whose only enforcement is one line, AccountReader.allowed():
#
#     return r != nil && r.granted != nil && r.granted()
#
# Every ReadHomeAccount and TranscriptStart call checks it before touching the
# filesystem. It is a guard #2095 ADDS, so there is no "before the fix" to run
# red; per AGENTS.md and docs/testing-philosophy.md it earns its place by being
# seen to go red when deleted. Deleting the grant clause makes the reader read
# ~/.claude.json while consent is pending or denied, which is exactly what
# TestAccountPermission_GateContract (contracttesting.AssertPermissionGated)
# exists to catch: "effect observed while permission \"account\" is pending".
#
# tools/mutate.sh owns the mechanics (stale/ambiguous-anchor guards,
# byte-for-byte restore, no git state touched); see
# tools/lib/quotainherit-account-equality-mutations_test.sh for the template.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: account-consent-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: account-consent-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "account-consent-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/account-consent-mutations_test.sh" >&2
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
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the tests failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | sed 's/^/      | /'
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

# ── The consent check is deleted ─────────────────────────────────────────────
#
# The anchor is the whole return line, which occurs exactly once in account.go.
assert_go_test_goes_red \
  "deleting the grant check lets the reader touch the identity file while pending" \
  "core/adapters/inbound/agents/claudecode/account.go" \
  'return r != nil && r.granted != nil && r.granted()' \
  'return r != nil' \
  "./core/adapters/inbound/agents/claudecode/" \
  'TestAccountPermission_GateContract' \
  "call site is not consent-gated"

# ── Codex: the same grant line, in its own reader ────────────────────────────
assert_go_test_goes_red \
  "deleting the codex grant check lets the reader touch auth.json while pending" \
  "core/adapters/inbound/agents/codex/account.go" \
  'return r != nil && r.granted != nil && r.granted()' \
  'return r != nil' \
  "./core/adapters/inbound/agents/codex/" \
  'TestAccountReader_GateContract' \
  "call site is not consent-gated"

# ── Pi/OpenCode recipient read in the inheritance pass ───────────────────────
#
# adapterGranted is the single per-adapter check recipientKey makes before
# opening a wrapper's auth.json. Deleting it makes every dashboard poll read
# ~/.pi/agent/auth.json and ~/.local/share/opencode/auth.json whatever the
# adapters' consent.
assert_go_test_goes_red \
  "deleting the recipient grant check lets the inheritance pass read wrapper auth.json without consent" \
  "core/application/services/quotainherit.go" \
  'return granted != nil && granted(adapter)' \
  'return true' \
  "./core/application/services/" \
  'TestInheritRateLimits_ReadsNoAuthFileWithoutConsent|TestInheritRateLimits_RecipientReadIsGatedPerAdapter|TestInheritRateLimits_NilGrantedReadsNothing' \
  "without consent"

if [[ $fails -gt 0 ]]; then
  echo "account-consent-mutations: $fails FAILED"
  exit 1
fi
echo "account-consent-mutations: ALL PASS"
