#!/usr/bin/env bash
# workerlist-waiting-swift-mutations_test.sh — the committed mutation fixtures
# for #2145's macOS half: a worker-list slot turning waiting notifies through
# the session funnel (SessionManager.waitingWorkerEdges / notifyWaitingWorkers
# in platforms/macos/Irrlicht/Managers/SessionManager+Notifications.swift), an
# orchestrator_updated push re-fetches /api/v1/sessions, and overlapping
# hydrates install in request order. Tests: SessionManagerWaitingWorkerTests
# and SessionManagerHydrateTests in platforms/macos/Tests/.
#
# WHY THIS FILE EXISTS. The LOCKs pass before and after the change by
# construction, and the other rules guard code that had no "before" of its
# own. Each earns its place by going red when the rule it protects is broken:
#   1. a slot first seen already waiting is a first sighting and stays silent;
#   2. the notifyOnWaiting toggle gates the worker notification;
#   3. a worker joined by a relayed session does not notify a second time;
#   4. a worker is waiting by its flag, never by its state text;
#   5. an older hydrate response is not installed over a newer one;
#   6. an orchestrator_updated push re-fetches /api/v1/sessions.
# Each was run with these exact anchors through tools/mutate.sh before this
# file was written, and went red with the messages asserted below.
#
# Runs on macos-latest only (test.yml's tools/lib/*_test.sh loop) — `swift` is
# not installed on the Linux job. The assert lives in swift-mutation-assert.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"
# shellcheck source=tools/lib/swift-mutation-assert.sh
. "$DIR/swift-mutation-assert.sh"

for tool in swift git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: workerlist-waiting-swift-mutations — $tool not found" >&2; exit 1; }
done
if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: workerlist-waiting-swift-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

# mutate.sh refuses a dirty tree; a dirty tree must not silently pass where
# the answer is load-bearing (see execution-confidence-swift-mutations_test.sh).
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "workerlist-waiting-swift-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/workerlist-waiting-swift-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0
NOTIFY="platforms/macos/Irrlicht/Managers/SessionManager+Notifications.swift"
COMPOSE="platforms/macos/Irrlicht/Managers/SessionManager+GroupComposition.swift"
HYDRATE="platforms/macos/Irrlicht/Managers/SessionManager+Hydration.swift"
WS="platforms/macos/Irrlicht/Managers/SessionManager+WebSocket.swift"

# ── 1. a slot absent from the old groups counts as an edge ──────────────────
assert_swift_test_goes_red \
  "counting a slot absent from the old groups notifies a first sighting" \
  "$NOTIFY" \
  '            if worker.isWaiting, before[edge] == false { edges.append(edge) }' \
  '            if worker.isWaiting, before[edge] != true { edges.append(edge) }' \
  "SessionManagerWaitingWorkerTests/testAFirstSightingAlreadyWaitingDoesNotFire" \
  'a slot first seen waiting notified'

# ── 2. the notifyOnWaiting toggle is not read ───────────────────────────────
assert_swift_test_goes_red \
  "dropping the notifyOnWaiting gate notifies with the toggle off" \
  "$NOTIFY" \
  '        guard !edges.isEmpty, defaults.bool(forKey: NotificationEvent.waiting.enabledKey) else { return }' \
  '        guard !edges.isEmpty else { return }' \
  "SessionManagerWaitingWorkerTests/testWaitingToggleOffDoesNotFire" \
  'notified with notifyOnWaiting off'

# ── 3. a joined worker's edge is not dropped ────────────────────────────────
assert_swift_test_goes_red \
  "notifying joined workers too fires twice for one waiting agent" \
  "$COMPOSE" \
  '        notifyWaitingWorkers(edges.filter { joined[$0.worker] == nil })' \
  '        notifyWaitingWorkers(edges)' \
  "SessionManagerWaitingWorkerTests/testAWorkerJoinedByARelayedSessionDoesNotFireTwice" \
  "a joined worker's waiting edge notified on top of its session's own notification"

# ── 4. the state text counts as waiting ─────────────────────────────────────
assert_swift_test_goes_red \
  "reading the state text as waiting notifies for a Gas Town string" \
  "$COMPOSE" \
  '        var isWaiting: Bool { waiting == true }' \
  '        var isWaiting: Bool { waiting == true || workerState == "waiting" }' \
  "SessionManagerWaitingWorkerTests/testAWorkerWhoseTextReadsWaitingWithoutTheFlagDoesNotFire" \
  'a worker without the waiting flag notified'

# ── 5. responses install in completion order ────────────────────────────────
assert_swift_test_goes_red \
  "dropping the request-order guard installs an older response over a newer one" \
  "$HYDRATE" \
  '            guard seq > lastInstalledHydrateSeq else { return }' \
  '            guard seq > 0 else { return }' \
  "SessionManagerHydrateTests/testAnOlderResponseIsNotInstalledOverANewerOne" \
  'the older response was installed over the newer one'

# ── 6. orchestrator_updated is ignored ──────────────────────────────────────
assert_swift_test_goes_red \
  "ignoring orchestrator_updated never re-fetches the sessions" \
  "$WS" \
  $'            // rehydrate.\n            scheduleRehydration()' \
  $'            // rehydrate.\n            break' \
  "SessionManagerHydrateTests/testAnOrchestratorUpdatePushRefetchesTheSessions" \
  'orchestrator_updated did not re-fetch /api/v1/sessions'

if [[ $fails -gt 0 ]]; then
  echo "workerlist-waiting-swift-mutations: $fails FAILED"
  exit 1
fi
echo "workerlist-waiting-swift-mutations: ALL PASS"
