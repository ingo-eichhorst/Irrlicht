#!/usr/bin/env bash
# keychain-test-isolation-swift-mutations_test.sh — the committed mutation
# fixtures for the two guards that keep `swift test` away from the
# developer's real login keychain and live daemon (KeychainStore's backend
# seam in platforms/macos/Irrlicht/Managers/KeychainStore.swift, DaemonManager's
# publisher seam in platforms/macos/Irrlicht/Managers/DaemonManager.swift;
# guards in platforms/macos/Tests/KeychainStoreBackendGuardTests.swift and
# platforms/macos/Tests/DaemonManagerPublisherGuardTests.swift).
#
# WHY THIS FILE EXISTS. Both guards are something the change ADDS, so they
# have no "before the fix" to run red; per AGENTS.md and
# docs/testing-philosophy.md each earns its place by being seen to fail when
# what it protects is broken. Five mutations, each run by hand with these
# exact anchors before this file was written, and red with the messages
# asserted below:
#   1. the process-wide KeychainStore goes back to the real keychain;
#   2. the backend rule is inverted;
#   3. the test-process detector (AppHome.isRunningTests) stops firing;
#   4. DaemonManager's default publisher goes back to the live PUT;
#   5. the publisher rule is inverted.
#
# SAFE TO RUN ON A DEVELOPER MAC, and only because of how it runs. Under
# mutations 1, 3 and 4 a KeychainStore.get or a DaemonManager publish in this
# process WOULD reach the real keychain (a securityd dialog) or the live
# daemon on :7837. Each mutation therefore runs exactly ONE guard test via
# `swift test --filter`, and every guard arm is inert by construction — it
# inspects which backend/publisher was selected and calls nothing on it (the
# guard classes' doc comments say so, and have no setUp). Never widen a
# filter below to a whole class or suite.
#
# Structure, the dirty-tree rule and the build-break check are copied from
# tools/lib/execution-confidence-swift-mutations_test.sh. Runs on
# macos-latest only (test.yml's tools/lib/*_test.sh loop) — `swift` is not
# installed on the Linux job.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: keychain-test-isolation-swift-mutations — $1 not found" >&2; exit 1; }; }
need swift
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: keychain-test-isolation-swift-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "keychain-test-isolation-swift-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/keychain-test-isolation-swift-mutations_test.sh" >&2
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

KS="platforms/macos/Irrlicht/Managers/KeychainStore.swift"
DM="platforms/macos/Irrlicht/Managers/DaemonManager.swift"
AH="platforms/macos/Irrlicht/Managers/AppHome.swift"

# ── 1. the shared store goes back to the real keychain ──────────────────────
assert_swift_test_goes_red \
  "restoring SystemKeychainBackend as the shared store's backend is caught" \
  "$KS" \
  $'    static let shared = KeychainSecrets(backend: defaultBackend(underXCTest: AppHome.isRunningTests))' \
  $'    static let shared = KeychainSecrets(backend: SystemKeychainBackend())' \
  "KeychainStoreBackendGuardTests/testTheSharedStoreUsesTheInMemoryBackendInATestProcess" \
  'KeychainStore.shared selected SystemKeychainBackend in a test process'

# ── 2. the backend rule is inverted ─────────────────────────────────────────
assert_swift_test_goes_red \
  "inverting the keychain backend rule is caught on both arms" \
  "$KS" \
  $'        underXCTest ? InMemoryKeychainBackend() : SystemKeychainBackend()' \
  $'        underXCTest ? SystemKeychainBackend() : InMemoryKeychainBackend()' \
  "KeychainStoreBackendGuardTests/testTheBackendRulePicksInMemoryUnderXCTestAndTheSystemKeychainInTheApp" \
  'a test process must get the in-memory backend'

# ── 3. the test-process detector stops firing ───────────────────────────────
assert_swift_test_goes_red \
  "a test-process detector that answers false inside XCTest is caught" \
  "$AH" \
  $'    static var isRunningTests: Bool { NSClassFromString("XCTestCase") != nil }' \
  $'    static var isRunningTests: Bool { NSClassFromString("XCTestCase") == nil }' \
  "KeychainStoreBackendGuardTests/testTheTestProcessDetectorFiresInThisVeryProcess" \
  'if the detector disagrees, KeychainStore.shared reads the user'

# ── 4. DaemonManager's default publisher goes back to the live PUT ─────────
assert_swift_test_goes_red \
  "restoring LiveRelayPublisher as DaemonManager's default is caught" \
  "$DM" \
  $'    init(publisher: RelayPublishing = DaemonManager.defaultPublisher(underXCTest: AppHome.isRunningTests),' \
  $'    init(publisher: RelayPublishing = LiveRelayPublisher(),' \
  "DaemonManagerPublisherGuardTests/testADefaultDaemonManagerInATestProcessDoesNotUseTheLivePublisher" \
  'DaemonManager() in a test process would PUT to the live daemon'

# ── 5. the publisher rule is inverted ───────────────────────────────────────
assert_swift_test_goes_red \
  "inverting the publisher rule is caught on both arms" \
  "$DM" \
  $'        underXCTest ? RecordingRelayPublisher() : LiveRelayPublisher()' \
  $'        underXCTest ? LiveRelayPublisher() : RecordingRelayPublisher()' \
  "DaemonManagerPublisherGuardTests/testThePublisherRuleRecordsUnderXCTestAndSendsInTheApp" \
  'a test process must get the recording publisher'

if [[ $fails -gt 0 ]]; then
  echo "keychain-test-isolation-swift-mutations: $fails FAILED"
  exit 1
fi
echo "keychain-test-isolation-swift-mutations: ALL PASS"
