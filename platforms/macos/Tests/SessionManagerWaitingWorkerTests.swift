import XCTest
@testable import Irrlicht

/// #2145: a worker-list slot whose agent waits for a person arrives as
/// `worker_state` "waiting". Installing the local daemon's groups raises the
/// same notification funnel a waiting session uses — once per edge into
/// waiting, never on a first sighting, never for Gas Town (whose worker
/// states are gt's own strings) and never with the waiting toggle off.
///
/// Evidence the funnel was entered is the read of the master-switch key:
/// `sendNotification` is its only reader inside `SessionManager`, and
/// `canUseUserNotifications` is false under `swift test`, so nothing is
/// posted (the same witness as `SessionManagerNotificationStoreTests`).
///
/// Red-first: the three "reaches the firing path" tests failed before the
/// change; the LOCKs pass there by construction, and three of them are held
/// by mutations instead (tools/lib/workerlist-waiting-swift-mutations_test.sh:
/// the Gas Town, first-sighting and toggle-off LOCKs each went red).
@MainActor
final class SessionManagerWaitingWorkerTests: XCTestCase {
    typealias AgentGroup = SessionManager.AgentGroup
    typealias Worker = SessionManager.UnattachedWorker

    private static func slot(_ name: String, _ state: String) -> Worker {
        Worker(role: "worker", icon: "\u{1F527}", description: "Pool worker slot",
               workerName: name, workerID: name, workerState: state)
    }

    private static func pool(_ workers: [Worker], type: String = "workerlist") -> [AgentGroup] {
        [AgentGroup(name: "VM tasks", type: type, groups: [
            AgentGroup(name: "lead-T100", status: "operational", unattachedWorkers: workers),
        ])]
    }

    private let bundleGuardMessage = """
        canUseUserNotifications is true in this test process — the runner gained \
        an app bundle, so these tests would post real notifications.
        """

    /// Installs `first`, then `second`, into a manager whose store enables
    /// `events`, and returns the keys the store was asked for during the
    /// second install only.
    private func keysAskedInstalling(_ first: [AgentGroup], then second: [AgentGroup],
                                     enabling events: [NotificationEvent] = [.waiting]) -> Set<String>? {
        let store = InMemoryDefaults()
        for event in events { store.set(true, forKey: event.enabledKey) }
        let manager = SessionManager(defaults: store)
        guard !manager.canUseUserNotifications else {
            XCTFail(bundleGuardMessage)
            return nil
        }
        manager.seedLocalApiGroups(first)
        let before = store.readKeys
        manager.seedLocalApiGroups(second)
        return store.readKeys.subtracting(before)
    }

    func testABusySlotTurningWaitingReachesTheFiringPath() {
        guard let asked = keysAskedInstalling(Self.pool([Self.slot("T101", "busy")]),
                                              then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertTrue(asked.contains(NotificationSettings.masterEnabledKey),
                      "busy → waiting never reached sendNotification; asked: \(asked.sorted())")
    }

    func testAFreeSlotTurningWaitingReachesTheFiringPath() {
        guard let asked = keysAskedInstalling(Self.pool([Self.slot("T101", "idle")]),
                                              then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertTrue(asked.contains(NotificationSettings.masterEnabledKey),
                      "idle → waiting never reached sendNotification; asked: \(asked.sorted())")
    }

    func testWaitingAfterBusyAgainReachesTheFiringPath() {
        let store = InMemoryDefaults()
        store.set(true, forKey: NotificationEvent.waiting.enabledKey)
        let manager = SessionManager(defaults: store)
        guard !manager.canUseUserNotifications else { return XCTFail(bundleGuardMessage) }
        manager.seedLocalApiGroups(Self.pool([Self.slot("T101", "waiting")]))
        manager.seedLocalApiGroups(Self.pool([Self.slot("T101", "busy")]))
        let before = store.readKeys
        manager.seedLocalApiGroups(Self.pool([Self.slot("T101", "waiting")]))
        XCTAssertTrue(store.readKeys.subtracting(before).contains(NotificationSettings.masterEnabledKey),
                      "a second waiting episode did not notify")
    }

    /// LOCK: the same waiting payload twice is one episode, not two.
    func testTheSameWaitingPayloadAgainDoesNotFire() {
        let waiting = Self.pool([Self.slot("T101", "waiting")])
        guard let asked = keysAskedInstalling(waiting, then: waiting) else { return }
        XCTAssertFalse(asked.contains(NotificationSettings.masterEnabledKey),
                       "an unchanged waiting slot notified again")
    }

    /// LOCK: a slot first seen already waiting — the first hydrate after
    /// launch — is a first sighting and stays silent, as a session's is.
    func testAFirstSightingAlreadyWaitingDoesNotFire() {
        guard let asked = keysAskedInstalling([], then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertFalse(asked.contains(NotificationSettings.masterEnabledKey),
                       "a slot first seen waiting notified")
    }

    /// LOCK: Gas Town passes gt's own state strings through; they carry no
    /// notification contract.
    func testAGasTownWorkerReadingWaitingDoesNotFire() {
        guard let asked = keysAskedInstalling(Self.pool([Self.slot("free-1", "busy")], type: "gastown"),
                                              then: Self.pool([Self.slot("free-1", "waiting")], type: "gastown"),
                                              enabling: NotificationEvent.allCases)
        else { return }
        XCTAssertFalse(asked.contains(NotificationSettings.masterEnabledKey),
                       "a Gas Town worker notified")
    }

    /// One edge per slot that turned waiting, identified by pool, team and
    /// slot: the notification's identifier and body come from it.
    func testWaitingWorkerEdgesNameEachSlotThatTurnedWaiting() {
        let edges = SessionManager.waitingWorkerEdges(
            from: Self.pool([Self.slot("T101", "busy"), Self.slot("T102", "busy"), Self.slot("T103", "waiting")]),
            to: Self.pool([Self.slot("T101", "waiting"), Self.slot("T102", "busy"), Self.slot("T103", "waiting")]))
        XCTAssertEqual(edges, [.init(pool: "VM tasks", team: "lead-T100", worker: "T101")])
        XCTAssertEqual(edges.first?.notificationIdentifier, "irrlicht-worker-VM tasks-lead-T100-T101")
        XCTAssertEqual(edges.first?.body, "lead-T100 \u{00B7} T101")
    }

    /// The row paints a slot as waiting only for the exact "waiting" string.
    func testOnlyAWaitingStateReadsAsWaiting() {
        XCTAssertTrue(Self.slot("T101", "waiting").isWaiting)
        for state in ["busy", "idle", "Waiting", ""] {
            XCTAssertFalse(Self.slot("T101", state).isWaiting, state)
        }
    }

    /// The hydrate polls every 2.5 s while any orchestrator group (a worker
    /// list or Gas Town) is listed, and every 30 s otherwise.
    func testHydratePollIntervalFollowsOrchestratorPresence() {
        let manager = SessionManager(defaults: InMemoryDefaults())
        XCTAssertEqual(manager.hydratePollInterval(for: Self.pool([Self.slot("T101", "busy")])), 2.5)
        XCTAssertEqual(manager.hydratePollInterval(for: Self.pool([], type: "gastown")), 2.5)
        XCTAssertEqual(manager.hydratePollInterval(for: [AgentGroup(name: "irrlicht", agents: [])]), 30)
        XCTAssertEqual(manager.hydratePollInterval(for: []), 30)
    }

    /// LOCK: with the waiting toggle off, the funnel is never entered.
    func testWaitingToggleOffDoesNotFire() {
        guard let asked = keysAskedInstalling(Self.pool([Self.slot("T101", "busy")]),
                                              then: Self.pool([Self.slot("T101", "waiting")]),
                                              enabling: [.ready])
        else { return }
        XCTAssertFalse(asked.contains(NotificationSettings.masterEnabledKey),
                       "notified with notifyOnWaiting off")
    }
}
