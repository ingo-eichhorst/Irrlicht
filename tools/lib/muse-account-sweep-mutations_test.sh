#!/usr/bin/env bash
# muse-account-sweep-mutations_test.sh — the committed mutation fixture for
# issue #2057's Muse account-quota sweep
# (core/application/services/museaccountsweep.go) and the detector write it
# goes through (core/application/services/session_detector_ratelimit.go).
#
# WHY THIS FILE EXISTS. Five of #2057's rules are guards with no "before the
# fix" to run red, because before #2057 no code wrote a Meta snapshot at all:
#   1. a failed poll publishes nothing (no zero snapshot);
#   2. without the Muse account-API grant the sweep clears the session's meta
#      snapshot (what makes a revoke take the chip away);
#   3. a meta write never replaces another provider's snapshot;
#   4. the daemon keeps the transport the sweep polls through;
#   5. an unchanged reading is not rewritten every tick.
# Per AGENTS.md and docs/testing-philosophy.md each one earns its place by
# going red when the thing it protects is broken, so each is broken below.
#
# tools/mutate.sh owns the mechanics (stale/ambiguous-anchor refusal and the
# byte-for-byte restore that never touches git state).

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

# A missing tool is a hard failure, not a skip — exiting 0 here would read as
# a PASS to preflight's shell_lib_tests, so the gate would go green having
# asserted nothing.
need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: muse-account-sweep-mutations — $1 not found" >&2; exit 1; }; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: muse-account-sweep-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "muse-account-sweep-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/muse-account-sweep-mutations_test.sh" >&2
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

# ── 1. A failed poll publishes a zero snapshot ──────────────────────────────
assert_go_test_goes_red \
  "a failed poll that writes an empty meta snapshot" \
  "core/application/services/museaccountsweep.go" \
  $'\t\t\ts.logFailure(st.SessionID, err)' \
  $'\t\t\ts.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMeta, &session.RateLimitSnapshot{Provider: session.ProviderMeta})' \
  "./core/application/services/" \
  'TestMuseAccountSweep_FailureWritesNothing' \
  "writes after a failed poll"

# ── 2. The not-granted branch is bypassed ───────────────────────────────────
# As run: the sweep then calls MuseAccountRefresh anyway, but the fetch is
# still refused — AccountPoller checks the same Granted func at admission —
# so the transport stays uncalled. What goes red is the missing clear.
assert_go_test_goes_red \
  "skipping the clear when the account-API grant is gone" \
  "core/application/services/museaccountsweep.go" \
  $'\t\tif !granted {' \
  $'\t\tif !granted && false {' \
  "./core/application/services/" \
  'TestMuseAccountSweep_NotGrantedClearsAndNeverFetches' \
  "want one meta clear for m1"

# ── 3. A meta write replaces another provider's snapshot ────────────────────
assert_go_test_goes_red \
  "dropping the other-provider guard in SetProviderRateLimit" \
  "core/application/services/session_detector_ratelimit.go" \
  'if current != nil && current.Provider != provider {' \
  'if false {' \
  "./core/application/services/" \
  'TestSetProviderRateLimit_NeverClobbersOtherProviderOrCreatesSession' \
  "a meta write replaced an anthropic snapshot"

# ── 4. The daemon drops the transport again (#2057's own defect) ───────────
# Before #2057, museAccountAPIEffects built the transport and threw it away,
# so nothing could poll. Discarding it again leaves sweeper() nil — no sweep.
assert_go_test_goes_red \
  "museAccountAPIEffects discarding the transport it builds" \
  "core/cmd/irrlichd/museaccountapi_effects.go" \
  $'\t\ttransport = t' \
  $'\t\t_ = t' \
  "./core/cmd/irrlichd/" \
  'TestMuseAccountAPI_SweeperIsBuiltWhenTheTransportIs' \
  "built no transport for the fixed Muse destination"

# ── 5. The sweep rewrites an unchanged reading every tick ───────────────────
assert_go_test_goes_red \
  "dropping the listed-reading check before a write" \
  "core/application/services/museaccountsweep.go" \
  'if snap != nil && (listed == nil || !sameRateLimitReading(listed, snap)) {' \
  'if snap != nil {' \
  "./core/application/services/" \
  'TestMuseAccountSweep_UnchangedReadingIsNotRewritten' \
  "after a tick with an unchanged reading"

# ── 6-17. Issue #2062: the credential is read once per grant ───────────────
# Each read on Muse's Keychain route raises a macOS dialog, so these guard
# GrantCredentialCache (core/application/services/grantcredential.go), the
# poller's credential feedback, the ErrKeychainRead marking, and the wiring
# that resets the cache. The sweep's first re-read after an auth rejection is
# not here: TestMuseAccountSweep_AuthRejectionReReadsCredentialOncePerGrant
# was seen red before anything invalidated the cache.

# 6. A failed Keychain read is re-read on the next resolve (the dialog loop).
assert_go_test_goes_red \
  "the grant cache not keeping a failed read" \
  "core/application/services/grantcredential.go" \
  'if r.err != nil && !c.keeps(r.err) {' \
  'if r.err != nil {' \
  "./core/application/services/" \
  'TestGrantCredentialCache_FailureIsStickyUntilReset|TestMuseAccountSweep_FailedResolveIsNotRetriedUntilReset' \
  "after a failure"

# 7. A failure that raised no dialog is kept (a login after the grant is
#    never picked up).
assert_go_test_goes_red \
  "the grant cache keeping every failure" \
  "core/application/services/grantcredential.go" \
  'return c.keepFailure != nil && c.keepFailure(err)' \
  'return true' \
  "./core/application/services/" \
  'TestGrantCredentialCache_UnkeptFailureIsReadAgain' \
  "after unkept failures, want 3"

# 8. Every caller starts its own read (two dialogs when Apply's warm-up and a
#    sweep tick overlap).
assert_go_test_goes_red \
  "the grant cache not sharing a read in flight" \
  "core/application/services/grantcredential.go" \
  $'\tif r == nil {' \
  $'\tif true {' \
  "./core/application/services/" \
  'TestGrantCredentialCache_ConcurrentCallersShareOneRead' \
  "concurrent callers, want 1"

# 9. Every auth rejection re-reads the credential.
assert_go_test_goes_red \
  "InvalidateOnce invalidating on every rejection" \
  "core/application/services/grantcredential.go" \
  'if c.reReadUsed {' \
  'if false {' \
  "./core/application/services/" \
  'TestMuseAccountSweep_AuthRejectionReReadsCredentialOncePerGrant' \
  "want 2 (the read, then one re-read)"

# 10. A rejection orphans a read still in flight (a second dialog).
assert_go_test_goes_red \
  "InvalidateOnce dropping a read in flight" \
  "core/application/services/grantcredential.go" \
  $'\t\tdefault:\n\t\t\treturn false' \
  $'\t\tdefault:' \
  "./core/application/services/" \
  'TestGrantCredentialCache_InvalidateOnceLeavesAReadInFlight' \
  "dropped a read still in flight"

# 11. A re-grant does not re-arm the re-read.
assert_go_test_goes_red \
  "Reset not re-arming InvalidateOnce" \
  "core/application/services/grantcredential.go" \
  $'\tc.cur = nil\n\tc.reReadUsed = false' \
  $'\tc.cur = nil' \
  "./core/application/services/" \
  'TestGrantCredentialCache_InvalidateOnceIsRearmedByReset' \
  "InvalidateOnce after a Reset did nothing"

# 12. An accepted fetch does not re-arm the re-read (a second token rotation
#     is never picked up).
assert_go_test_goes_red \
  "CredentialAccepted not re-arming InvalidateOnce" \
  "core/application/services/grantcredential.go" \
  $'\tdefer c.mu.Unlock()\n\tc.reReadUsed = false\n}' \
  $'\tdefer c.mu.Unlock()\n}' \
  "./core/application/services/" \
  'TestMuseAccountSweep_AcceptedFetchRearmsTheReRead' \
  "want 3 (read, re-read, re-read after the second rotation)"

# 13. A Keychain lookup failure is not marked, so it would be retried.
assert_go_test_goes_red \
  "the Muse resolver not marking a Keychain lookup failure" \
  "core/adapters/outbound/museaccountapi/credential.go" \
  'fmt.Errorf("%w: lookup: %w", ErrKeychainRead, err)' \
  'fmt.Errorf("museaccountapi: keychain lookup: %w", err)' \
  "./core/adapters/outbound/museaccountapi/" \
  'TestCredentialResolver_OnlyKeychainFailuresAreMarked' \
  "want it marked ErrKeychainRead"

# 14. A revoke keeps the credential cached.
assert_go_test_goes_red \
  "museAccountAPIEffects' Stop not dropping the credential" \
  "core/cmd/irrlichd/museaccountapi_effects.go" \
  $'\t\tresolver.Reset()\n\t\treturn nil' \
  $'\t\treturn nil' \
  "./core/cmd/irrlichd/" \
  'TestMuseAccountAPI_RevokeDropsTheCredential' \
  "a resolve after a revoke was served the credential cached before it"

# 15. A grant keeps whatever was cached before it.
assert_go_test_goes_red \
  "museAccountAPIEffects' Start not resetting the cache" \
  "core/cmd/irrlichd/museaccountapi_effects.go" \
  $'\t\tresolver.Reset()\n\t\tgo func() {' \
  $'\t\tgo func() {' \
  "./core/cmd/irrlichd/" \
  'TestMuseAccountAPI_ApplyRereadsTheCredential' \
  "credential reads, want 2"

# 16. AccountPoller stops reporting a rejection to the resolver (the rejected
#     token is never re-read).
assert_go_test_goes_red \
  "AccountPoller not reporting an auth rejection" \
  "core/application/services/accountpoller.go" \
  'if f, ok := req.Resolver.(CredentialFeedback); ok && reason == outbound.QuotaFailureAuthRejected {' \
  'if f, ok := req.Resolver.(CredentialFeedback); ok && false {' \
  "./core/application/services/" \
  'TestMuseAccountSweep_AuthRejectionReReadsCredentialOncePerGrant' \
  "want 2 (the read, then one re-read)"

# 17. AccountPoller stops reporting an accepted fetch (a second rotation is
#     never picked up).
assert_go_test_goes_red \
  "AccountPoller not reporting an accepted credential" \
  "core/application/services/accountpoller.go" \
  $'\tif f, ok := req.Resolver.(CredentialFeedback); ok {\n\t\tf.CredentialAccepted()' \
  $'\tif f, ok := req.Resolver.(CredentialFeedback); ok && false {\n\t\tf.CredentialAccepted()' \
  "./core/application/services/" \
  'TestMuseAccountSweep_AcceptedFetchRearmsTheReRead' \
  "want 3 (read, re-read, re-read after the second rotation)"

if [[ $fails -gt 0 ]]; then
  echo "muse-account-sweep-mutations: $fails FAILED"
  exit 1
fi
echo "muse-account-sweep-mutations: ALL PASS"
