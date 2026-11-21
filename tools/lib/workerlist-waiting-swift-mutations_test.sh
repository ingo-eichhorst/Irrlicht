#!/usr/bin/env bash
# workerlist-waiting-swift-mutations_test.sh — the committed mutation fixtures
# for #2145's macOS half: a worker-list slot turning waiting notifies through
# the session funnel (SessionManager.waitingWorkerEdges / notifyWaitingWorkers
# in platforms/macos/Irrlicht/Managers/SessionManager+Notifications.swift,
# tests in platforms/macos/Tests/SessionManagerWaitingWorkerTests.swift).
#
# WHY THIS FILE EXISTS. The three LOCK tests below pin limits #2145 ADDS, so
# they pass before and after the change by construction; each earns its place
# by going red when the rule it protects is broken:
#   1. only worker-list groups notify (Gas Town's states are gt's own);
#   2. a slot first seen already waiting is a first sighting and stays silent;
#   3. the notifyOnWaiting toggle gates the worker notification.
# Each was run with these exact anchors through tools/mutate.sh before this
# file was written, and went red with the messages asserted below.
#
# Structure, the dirty-tree rule and the build-break check are copied from
# tools/lib/execution-confidence-swift-mutations_test.sh. Runs on
# macos-latest only (test.yml's tools/lib/*_test.sh loop) — `swift` is not
# installed on the Linux job.
set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() { command -v "$1" >/dev/null 2>&1 || { echo "FAIL: workerlist-waiting-swift-mutations — $1 not found" >&2; exit 1; }; }
need swift
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: workerlist-waiting-swift-mutations — $MUTATE_SH is missing or not executable" >&2
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
  echo "workerlist-waiting-swift-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/workerlist-waiting-swift-mutations_test.sh" >&2
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

NOTIFY="platforms/macos/Irrlicht/Managers/SessionManager+Notifications.swift"

# ── 1. every orchestrator's slots count, not only a worker list's ───────────
assert_swift_test_goes_red \
  "dropping the workerlist filter lets a Gas Town worker notify" \
  "$NOTIFY" \
  '        for pool in groups where pool.type == "workerlist" {' \
  '        for pool in groups {' \
  "SessionManagerWaitingWorkerTests/testAGasTownWorkerReadingWaitingDoesNotFire" \
  'a Gas Town worker notified'

# ── 2. a slot absent from the old groups counts as an edge ──────────────────
assert_swift_test_goes_red \
  "counting a slot absent from the old groups notifies a first sighting" \
  "$NOTIFY" \
  '            if worker.isWaiting, before[edge] == false { edges.append(edge) }' \
  '            if worker.isWaiting, before[edge] != true { edges.append(edge) }' \
  "SessionManagerWaitingWorkerTests/testAFirstSightingAlreadyWaitingDoesNotFire" \
  'a slot first seen waiting notified'

# ── 3. the notifyOnWaiting toggle is not read ───────────────────────────────
assert_swift_test_goes_red \
  "dropping the notifyOnWaiting gate notifies with the toggle off" \
  "$NOTIFY" \
  '        guard !edges.isEmpty, defaults.bool(forKey: NotificationEvent.waiting.enabledKey) else { return }' \
  '        guard !edges.isEmpty else { return }' \
  "SessionManagerWaitingWorkerTests/testWaitingToggleOffDoesNotFire" \
  'notified with notifyOnWaiting off'

if [[ $fails -gt 0 ]]; then
  echo "workerlist-waiting-swift-mutations: $fails FAILED"
  exit 1
fi
echo "workerlist-waiting-swift-mutations: ALL PASS"
