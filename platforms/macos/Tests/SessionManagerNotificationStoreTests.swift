import XCTest
@testable import Irrlicht

/// #1693: the notification firing path must decide from the store the manager
/// was GIVEN, never from the process preference domain.
///
/// # The defect
///
/// One method read two stores. `checkStateTransitionNotification` took
/// `notifyOnReady` / `notifyOnWaiting` off `self.defaults` — the store #1662
/// gave `SessionManager` so a test would not have to write the developer's real
/// domain — and the `sendNotification` it then called guarded on
/// `NotificationSettings.masterEnabled()`, whose default argument was
/// `UserDefaults.standard`. Under `swift test` that is
/// `com.apple.dt.xctest.tool`, a plist in the developer's own
/// `~/Library/Preferences`; on the machine this was found on it holds
/// `notificationsEnabled = 1`, so the gate answered "yes" whatever a fixture
/// arranged. It is #1689's shape one call site further on: a guard gated on the
/// right RULE and reading the wrong STORE.
///
/// # What is observable here, and what is not — stated rather than left implied
///
/// `sendNotification`'s second guard is `canUseUserNotifications`, which is
/// false outside an app bundle, so under `swift test` the method returns before
/// it can schedule a `UNNotificationRequest` or reach `SoundPlayer.speak`.
/// There is therefore no downstream VALUE to assert on and the gate's decision
/// is genuinely unobservable here — which is exactly why nothing graded this
/// call site before.
///
/// The READ is observable. `InMemoryDefaults.readKeys` records every key the
/// store was asked for, and "which store was asked" *is* the defect, not a
/// proxy for it. Each arm therefore asserts on the keys the driven store was
/// asked for during the call, and each takes a DELTA around the call rather
/// than the whole set, so `SessionManager.init`'s own reads cannot be mistaken
/// for the gate's.
///
/// Both arms assert `canUseUserNotifications` is false rather than assuming it:
/// a runner that ever gained a bundle would otherwise fire real notifications
/// at a developer and call `UNUserNotificationCenter.current()`, which traps
/// outside one. That precondition's own mutation — making the property true —
/// is not run, and the reason is recorded rather than skipped: it would make
/// `setupNotificationDelegate()` reach `UNUserNotificationCenter.current()`
/// from `init`, which aborts the whole runner, so the mutation destroys the
/// harness instead of grading it.
@MainActor
final class SessionManagerNotificationStoreTests: XCTestCase {

    // MARK: - Arms

    /// The firing path's master gate asks the store the manager was given.
    ///
    /// Driven at `sendNotification` itself, the funnel every notification leaves
    /// through, so the arm does not depend on any particular caller reaching it.
    ///
    /// The arrangement is the upgrade-fallback one `NotificationSettings`
    /// exists for: `notificationsEnabled` absent, no event enabled. That makes
    /// the gate answer *false*, so the method returns AT the gate — nothing
    /// downstream is entered at all — and it is also the arrangement under
    /// which `masterEnabled(defaults:)` reads the most keys, because
    /// `allCases.contains` short-circuits on the first enabled event and here
    /// there is none.
    func testTheMasterGateAsksTheStoreTheManagerWasGiven() {
        let (manager, store) = arrangedManager()
        guard !manager.canUseUserNotifications else {
            return XCTFail(bundleGuardMessage)
        }

        let before = store.readKeys
        manager.sendNotification(
            identifier: "irrlicht-state-sess_1693",
            title: "Agent ready",
            body: "irrlicht (main)",
            sessionID: "sess_1693",
            event: .ready)
        let asked = store.readKeys.subtracting(before)

        XCTAssertTrue(
            asked.contains(NotificationSettings.masterEnabledKey),
            """
            sendNotification's master gate never asked the store this manager \
            was GIVEN for \(NotificationSettings.masterEnabledKey). It is \
            deciding from another one — UserDefaults.standard, which under \
            `swift test` is the developer's com.apple.dt.xctest.tool and right \
            now holds \(NotificationSettings.masterEnabledKey) = \
            \(String(describing: UserDefaults.standard.object(forKey: NotificationSettings.masterEnabledKey))), \
            so the gate answers that machine's setting whatever this fixture \
            arranges. Everything the call did ask this store for: \
            \(asked.sorted()).
            """)

        XCTAssertEqual(
            asked, referenceReads(),
            """
            The gate asked this store, but not the way \
            NotificationSettings.masterEnabled(defaults:) asks one. Driving \
            sendNotification read \(asked.sorted()); calling the app's own rule \
            over an identically arranged store reads \(referenceReads().sorted()). \
            A read that does not DECIDE — the master key fetched and thrown \
            away while the guard still resolves another store — satisfies the \
            assertion above and is what this one separates out. The expected \
            set is produced by calling the rule, never written down, so a \
            fourth NotificationEvent is covered by existing.
            """)
    }

    /// The two store reads inside one firing path land in one store — which is
    /// the asymmetry #1693 is about, driven end to end through the production
    /// entry point rather than at the funnel.
    ///
    /// `checkStateTransitionNotification` reads `notifyOnReady` /
    /// `notifyOnWaiting` off `self.defaults` and then calls `sendNotification`,
    /// whose gate read the process domain. Same object, same call chain, two
    /// stores. The first assertion is the vacuity guard: without it a
    /// transition that never reached the firing path at all reports the same
    /// verdict as one whose gate read the wrong store.
    func testTheTwoStoreReadsInOneFiringPathLandInTheSameStore() {
        let (manager, store) = arrangedManager(enabling: [.ready])
        guard !manager.canUseUserNotifications else {
            return XCTFail(bundleGuardMessage)
        }

        let before = store.readKeys
        manager.checkStateTransitionNotification(session: readySession(), previousState: .working)
        let asked = store.readKeys.subtracting(before)

        XCTAssertTrue(
            asked.contains(NotificationEvent.ready.enabledKey),
            """
            checkStateTransitionNotification never asked this store for \
            \(NotificationEvent.ready.enabledKey), so it did not reach the \
            firing path and this arm proves nothing about the gate. Everything \
            the call asked this store for: \(asked.sorted()).
            """)

        XCTAssertTrue(
            asked.contains(NotificationSettings.masterEnabledKey),
            """
            One method, two stores: checkStateTransitionNotification read \
            \(NotificationEvent.ready.enabledKey) off the store it was given \
            and the sendNotification it called then decided from another one. \
            UserDefaults.standard holds \
            \(NotificationSettings.masterEnabledKey) = \
            \(String(describing: UserDefaults.standard.object(forKey: NotificationSettings.masterEnabledKey))) \
            here, so the gate's answer comes off this machine. Everything the \
            call asked this store for: \(asked.sorted()).
            """)
    }

    // MARK: - Fixtures

    /// A manager over a store in the condition the app puts one in — nothing
    /// but `SessionManager`'s own `register(defaults:)` seed — plus the named
    /// events switched on.
    ///
    /// `masterEnabledKey` is deliberately never seeded, matching the app: it is
    /// kept out of that seed on purpose so `object(forKey:)` can still answer
    /// nil and the upgrade fallback stays reachable.
    // MARK: - #1802: the fourth state must not swallow a recovery notification

    /// Drives one state transition and returns the keys the DRIVEN store was
    /// asked for during the call — a delta, so `SessionManager.init`'s own
    /// reads cannot be mistaken for the gate's.
    ///
    /// Reaching the firing path is what makes `sendNotification` ask for the
    /// master key, so that read IS the evidence the switch arm was entered.
    /// Asserting on a downstream value is impossible here —
    /// `canUseUserNotifications` is false outside an app bundle — and the
    /// precondition is checked rather than assumed, so a runner that ever
    /// gained a bundle fails loudly instead of firing real notifications at a
    /// developer. Returns nil in that case, having already failed.
    ///
    /// `arrangedManager(enabling:)` sets each event's own `enabledKey`, so no
    /// caller here re-types `"notifyOnWaiting"` — the enum owns that string.
    private func keysAskedDriving(
        _ session: SessionState,
        from previousState: SessionState.State,
        enabling events: [NotificationEvent]
    ) -> Set<String>? {
        let (manager, store) = arrangedManager(enabling: events)
        guard !manager.canUseUserNotifications else {
            XCTFail(bundleGuardMessage)
            return nil
        }
        let before = store.readKeys
        manager.checkStateTransitionNotification(session: session, previousState: previousState)
        return store.readKeys.subtracting(before)
    }

    /// `working → error → waiting` must still announce "Agent waiting for
    /// input".
    ///
    /// `previousState` is the immediately-previous LIVE state. Before #1802 the
    /// two arms required `previousState == .working` literally, so once the
    /// fourth state could sit between them the transition arrived with
    /// `previousState == .error` and fell through `default: return` — silently
    /// dropping a notification the user got before #1798 existed. Latent when
    /// #1802 was written (no adapter produced `.error` yet) and live the moment
    /// #1799/#1800 land, which is precisely the kind of gap that goes unnoticed.
    ///
    /// Mutation check (verified): restore `previousState == .working` on the
    /// `.waiting` arm and this goes red — the master key is never asked for.
    func testAWaitingTransitionOutOfErrorStillNotifies() {
        guard let asked = keysAskedDriving(waitingSession(), from: .error, enabling: [.waiting])
        else { return }

        XCTAssertTrue(
            asked.contains(NotificationSettings.masterEnabledKey),
            """
            checkStateTransitionNotification never reached the firing path for \
            working → error → waiting, so the "Agent waiting for input" \
            notification was silently dropped. Everything the call asked this \
            store for: \(asked.sorted()).
            """)
    }

    /// The same for `working → error → ready`.
    func testAReadyTransitionOutOfErrorStillNotifies() {
        guard let asked = keysAskedDriving(readySession(), from: .error, enabling: [.ready])
        else { return }

        XCTAssertTrue(
            asked.contains(NotificationSettings.masterEnabledKey),
            "working → error → ready did not reach the firing path; asked: \(asked.sorted()).")
    }

    /// LOCK — ENTERING `.error` still notifies nothing. Only the SOURCE side of
    /// the two arms widened; #1802 deliberately adds no error notification, and
    /// a retrying error flaps, so one would be noise. Driven with EVERY event
    /// enabled, so the silence is the switch's doing and not a disabled toggle.
    func testEnteringErrorNotifiesNothing() {
        guard let asked = keysAskedDriving(erroredSession(), from: .working,
                                           enabling: NotificationEvent.allCases)
        else { return }

        XCTAssertFalse(
            asked.contains(NotificationSettings.masterEnabledKey),
            "entering .error reached the firing path — #1802 adds no error notification.")
    }

    private func waitingSession() -> SessionState {
        readySession().withState(.waiting)
    }

    private func erroredSession() -> SessionState {
        readySession().withState(.error)
    }

    private func arrangedManager(enabling events: [NotificationEvent] = [])
        -> (manager: SessionManager, store: InMemoryDefaults) {
        let store = InMemoryDefaults()
        for event in events { store.set(true, forKey: event.enabledKey) }
        return (SessionManager(defaults: store), store)
    }

    /// The keys `NotificationSettings.masterEnabled(defaults:)` asks a store
    /// for, measured by asking a second, identically arranged one — so the
    /// expected set is produced by the app's own rule instead of being a key
    /// list this file keeps in sync by hand.
    private func referenceReads(enabling events: [NotificationEvent] = []) -> Set<String> {
        let store = InMemoryDefaults()
        for event in events { store.set(true, forKey: event.enabledKey) }
        _ = SessionManager(defaults: store)
        let before = store.readKeys
        _ = NotificationSettings.masterEnabled(defaults: store)
        return store.readKeys.subtracting(before)
    }

    private func readySession() -> SessionState {
        SessionState(
            id: "sess_1693",
            state: .ready,
            model: "claude-opus-4-7",
            cwd: "/Users/test/projects/irrlicht",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            transcriptPath: nil,
            gitBranch: "main",
            projectName: "irrlicht",
            firstSeen: Date(timeIntervalSince1970: 1_700_000_000),
            updatedAt: Date(timeIntervalSince1970: 1_700_000_000),
            eventCount: 0,
            lastEvent: nil)
    }

    private let bundleGuardMessage = """
        canUseUserNotifications is TRUE under this runner. These arms drive \
        sendNotification, which past that guard schedules a real \
        UNNotificationRequest and calls UNUserNotificationCenter.current() — at \
        a developer, from a test. Refusing rather than running: the arms below \
        assert on which store the gate read, and neither needs the notification \
        to be delivered.
        """
}
