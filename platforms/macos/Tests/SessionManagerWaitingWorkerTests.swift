import XCTest
@testable import Irrlicht

/// #2145: a worker-list slot whose agent waits for a person arrives with
/// `waiting: true` (and `worker_state` "waiting" as its text). Installing the
/// local daemon's groups raises the same notification funnel a waiting session
/// uses — once per edge into waiting, never on a first sighting, never for a
/// worker without the flag (Gas Town passes gt's own state strings through in
/// `worker_state`), never for a worker a relayed session joined, and never with
/// the waiting toggle off.
///
/// Evidence the funnel was entered is the read of the master-switch key:
/// `sendNotification` is its only reader inside `SessionManager`, and
/// `canUseUserNotifications` is false under `swift test`, so nothing is
/// posted (the same witness as `SessionManagerNotificationStoreTests`).
///
/// Red-first: the three "reaches the firing path" tests and the joined-worker
/// test failed before their changes; the LOCKs pass there by construction and
/// are held by mutations instead (tools/lib/workerlist-waiting-swift-mutations_test.sh).
@MainActor
final class SessionManagerWaitingWorkerTests: XCTestCase {
    typealias AgentGroup = SessionManager.AgentGroup
    typealias Worker = SessionManager.UnattachedWorker

    /// A slot as the daemon sends it: `waiting` set exactly when the worker
    /// list said so. `flag: false` is a worker whose text merely reads
    /// "waiting", as a Gas Town state passed through would.
    private static func slot(_ name: String, _ state: String, flag: Bool? = nil) -> Worker {
        var w = Worker(role: "worker", icon: "\u{1F527}", description: "Pool worker slot",
                       workerName: name, workerID: name, workerState: state)
        w.waiting = (flag ?? (state == "waiting")) ? true : nil
        return w
    }

    private static func pool(_ workers: [Worker], type: String = "workerlist") -> [AgentGroup] {
        [AgentGroup(name: "VM tasks", type: type, groups: [
            AgentGroup(name: "lead-T100", status: "operational", unattachedWorkers: workers),
        ])]
    }

    /// Installs each of `prior` in turn, runs `before`, then installs `last`,
    /// in a manager whose store enables `events`; returns the keys the store
    /// was asked for during the last install only, or nil having failed.
    private func keysAskedInstalling(_ prior: [[AgentGroup]], then last: [AgentGroup],
                                     enabling events: [NotificationEvent] = [.waiting],
                                     before: (SessionManager) -> Void = { _ in }) -> Set<String>? {
        let store = InMemoryDefaults()
        for event in events { store.set(true, forKey: event.enabledKey) }
        let manager = SessionManager(defaults: store)
        guard !manager.canUseUserNotifications else {
            XCTFail("canUseUserNotifications is true in this test process — the runner gained an app bundle, so these tests would post real notifications.")
            return nil
        }
        prior.forEach { manager.seedLocalApiGroups($0) }
        before(manager)
        let seen = store.readKeys
        // readKeys only grows: a key already read would hide a second read.
        guard !seen.contains(NotificationSettings.masterEnabledKey) else {
            XCTFail("the master key was read before the install under test, so the delta cannot show it")
            return nil
        }
        manager.seedLocalApiGroups(last)
        return store.readKeys.subtracting(seen)
    }

    private func fired(_ asked: Set<String>) -> Bool { asked.contains(NotificationSettings.masterEnabledKey) }

    func testABusySlotTurningWaitingReachesTheFiringPath() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("T101", "busy")])],
                                              then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertTrue(fired(asked), "busy → waiting never reached sendNotification; asked: \(asked.sorted())")
    }

    func testAFreeSlotTurningWaitingReachesTheFiringPath() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("T101", "idle")])],
                                              then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertTrue(fired(asked), "idle → waiting never reached sendNotification; asked: \(asked.sorted())")
    }

    func testWaitingAfterBusyAgainReachesTheFiringPath() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("T101", "waiting")]),
                                               Self.pool([Self.slot("T101", "busy")])],
                                              then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertTrue(fired(asked), "a second waiting episode did not notify")
    }

    /// LOCK: the same waiting payload twice is one episode, not two.
    func testTheSameWaitingPayloadAgainDoesNotFire() {
        let waiting = Self.pool([Self.slot("T101", "waiting")])
        guard let asked = keysAskedInstalling([waiting], then: waiting) else { return }
        XCTAssertFalse(fired(asked), "an unchanged waiting slot notified again")
    }

    /// LOCK: a slot first seen already waiting — the first hydrate after
    /// launch — is a first sighting and stays silent, as a session's is.
    func testAFirstSightingAlreadyWaitingDoesNotFire() {
        guard let asked = keysAskedInstalling([[]], then: Self.pool([Self.slot("T101", "waiting")]))
        else { return }
        XCTAssertFalse(fired(asked), "a slot first seen waiting notified")
    }

    /// LOCK: a Gas Town worker whose passed-through state text reads
    /// "waiting" carries no flag, so it neither notifies nor paints.
    func testAWorkerWhoseTextReadsWaitingWithoutTheFlagDoesNotFire() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("free-1", "busy")], type: "gastown")],
                                              then: Self.pool([Self.slot("free-1", "waiting", flag: false)], type: "gastown"),
                                              enabling: NotificationEvent.allCases)
        else { return }
        XCTAssertFalse(fired(asked), "a worker without the waiting flag notified")
        XCTAssertFalse(Self.slot("free-1", "waiting", flag: false).isWaiting, "a worker without the flag reads as waiting")
    }

    /// A worker whose relayed session is joined into its row (#2149) shows
    /// that session, which notifies for itself; the worker's edge stays
    /// silent rather than notifying a second time.
    func testAWorkerJoinedByARelayedSessionDoesNotFireTwice() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("T101", "busy")])],
                                              then: Self.pool([Self.slot("T101", "waiting")]),
                                              before: { manager in
            manager.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "T101")]))
            manager.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "vm-proj"))
            XCTAssertNotNil(manager.relayWorkerJoins()["T101"], "precondition: the relayed session joined T101")
        })
        else { return }
        XCTAssertFalse(fired(asked), "a joined worker's waiting edge notified on top of its session's own notification")
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

    /// The row paints a worker as waiting from the flag, never the text.
    func testOnlyTheWaitingFlagReadsAsWaiting() {
        XCTAssertTrue(Self.slot("T101", "waiting").isWaiting)
        XCTAssertFalse(Self.slot("T101", "waiting", flag: false).isWaiting)
        XCTAssertFalse(Self.slot("T101", "busy").isWaiting)
    }

    /// LOCK: with the waiting toggle off, the funnel is never entered.
    func testWaitingToggleOffDoesNotFire() {
        guard let asked = keysAskedInstalling([Self.pool([Self.slot("T101", "busy")])],
                                              then: Self.pool([Self.slot("T101", "waiting")]),
                                              enabling: [.ready])
        else { return }
        XCTAssertFalse(fired(asked), "notified with notifyOnWaiting off")
    }
}
