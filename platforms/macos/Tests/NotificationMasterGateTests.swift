import XCTest
@testable import Irrlicht

/// Regression coverage for #1183: the master "Enable notifications" toggle was
/// wired only to the Settings UI, so switching it off collapsed the section but
/// left the per-event keys `true` underneath — banners kept posting and, most
/// noticeably, the Speak-aloud voice kept talking.
///
/// `SessionManager.sendNotification` now guards on
/// `NotificationSettings.masterEnabled()`. That funnel is where both the
/// `UNNotificationRequest` (banner + sound) and the separate `SoundPlayer.speak`
/// call live, so one gate covers every channel. The funnel itself can't run
/// under xctest (`canUseUserNotifications` is false outside an app bundle),
/// which is why the decision is extracted and asserted directly — the same
/// pure-helper idiom `SessionManagerFocusTests` uses for the Focus/DND gate.
final class NotificationMasterGateTests: XCTestCase {

    private var suiteName: String!
    private var defaults: UserDefaults!

    override func setUp() {
        super.setUp()
        // A throwaway suite, never `.standard` — these tests must not mutate the
        // developer's real notification preferences.
        suiteName = "io.irrlicht.tests.NotificationMasterGate.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)
    }

    override func tearDown() {
        defaults.removePersistentDomain(forName: suiteName)
        defaults = nil
        suiteName = nil
        super.tearDown()
    }

    // MARK: - Pure decision

    /// The reported bug. Turning the master off never cleared the per-event
    /// keys, so "off" has to win over them rather than be ignored.
    func testMasterOffSuppressesEvenWithEveryEventEnabled() {
        XCTAssertFalse(NotificationSettings.masterEnabled(master: false, anyEventEnabled: true))
    }

    func testMasterOnFires() {
        XCTAssertTrue(NotificationSettings.masterEnabled(master: true, anyEventEnabled: true))
        XCTAssertTrue(NotificationSettings.masterEnabled(master: true, anyEventEnabled: false))
    }

    /// Upgrade path: the key is absent for anyone who configured notifications
    /// before #940 and hasn't opened Settings since. Absent must mean "keep
    /// firing", not "silence" — otherwise this fix would break working setups.
    func testAbsentMasterFallsBackToPerEventOr() {
        XCTAssertTrue(NotificationSettings.masterEnabled(master: nil, anyEventEnabled: true))
        XCTAssertFalse(NotificationSettings.masterEnabled(master: nil, anyEventEnabled: false))
    }

    // MARK: - UserDefaults read

    func testDefaultsReadWithMasterOffSuppressesEnabledEvents() {
        defaults.set(true, forKey: NotificationEvent.ready.enabledKey)
        defaults.set(true, forKey: NotificationEvent.waiting.enabledKey)
        defaults.set(true, forKey: NotificationEvent.contextPressure.enabledKey)
        defaults.set(false, forKey: NotificationSettings.masterEnabledKey)

        XCTAssertFalse(NotificationSettings.masterEnabled(defaults: defaults))
    }

    func testDefaultsReadWithMasterOnFires() {
        defaults.set(true, forKey: NotificationSettings.masterEnabledKey)

        XCTAssertTrue(NotificationSettings.masterEnabled(defaults: defaults))
    }

    func testDefaultsReadWithAbsentMasterFollowsEnabledEvents() {
        defaults.set(true, forKey: NotificationEvent.waiting.enabledKey)

        XCTAssertTrue(NotificationSettings.masterEnabled(defaults: defaults))
    }

    func testDefaultsReadWithAbsentMasterAndNoEventsStaysSilent() {
        XCTAssertFalse(NotificationSettings.masterEnabled(defaults: defaults))
    }

    /// Lock-in for the reason the helper reads `object(forKey:)`: `bool(forKey:)`
    /// reports the same `false` for "absent" and for "explicitly off", which
    /// would collapse the upgrade fallback into a silent-by-default gate.
    func testAbsentKeyIsDistinguishableFromExplicitFalse() {
        defaults.set(true, forKey: NotificationEvent.ready.enabledKey)
        XCTAssertNil(defaults.object(forKey: NotificationSettings.masterEnabledKey))
        XCTAssertFalse(defaults.bool(forKey: NotificationSettings.masterEnabledKey))
        XCTAssertTrue(NotificationSettings.masterEnabled(defaults: defaults))

        defaults.set(false, forKey: NotificationSettings.masterEnabledKey)
        XCTAssertNotNil(defaults.object(forKey: NotificationSettings.masterEnabledKey))
        XCTAssertFalse(NotificationSettings.masterEnabled(defaults: defaults))
    }

    /// The key is user-persisted state: renaming it would orphan every existing
    /// user's setting and silently re-run the one-time reconcile.
    func testMasterEnabledKeyIsStable() {
        XCTAssertEqual(NotificationSettings.masterEnabledKey, "notificationsEnabled")
    }
}
