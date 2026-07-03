import XCTest
import UserNotifications
@testable import Irrlicht

/// Regression coverage for #338: when macOS Focus / DND is active, notification
/// sound must be suppressed alongside the (system-suppressed) banner — for
/// both built-in/custom sounds (gated in `willPresent` via
/// `presentationOptions`) and TTS (gated in `sendNotification` via
/// `voiceForSpeak`).
final class SessionManagerFocusTests: XCTestCase {

    // MARK: - presentationOptions (willPresent gating)

    func testPresentationOptionsUnderFocusSuppressesEverything() {
        XCTAssertEqual(NotificationClickForwarder.presentationOptions(focusActive: true), [])
    }

    func testPresentationOptionsWithoutFocusShowsBannerAndSound() {
        XCTAssertEqual(NotificationClickForwarder.presentationOptions(focusActive: false), [.banner, .sound])
    }

    // MARK: - voiceForSpeak (TTS gating)

    func testVoiceForSpeakReturnsVoiceWhenFocusOff() {
        let result = SessionManager.voiceForSpeak(choice: .speak(.female), focusActive: false)
        XCTAssertEqual(result, .female)
    }

    func testVoiceForSpeakReturnsNilWhenFocusOn() {
        XCTAssertNil(SessionManager.voiceForSpeak(choice: .speak(.female), focusActive: true))
    }

    func testVoiceForSpeakReturnsNilForNonSpeakChoices() {
        XCTAssertNil(SessionManager.voiceForSpeak(choice: .chime, focusActive: false))
        XCTAssertNil(SessionManager.voiceForSpeak(choice: .none, focusActive: false))
        XCTAssertNil(SessionManager.voiceForSpeak(choice: .ping, focusActive: true))
    }

    // MARK: - FocusMonitor xctest-safety guard

    /// Lock-in for the `isAppContext` guard in `FocusMonitor`. Without it,
    /// constructing FocusMonitor inside xctest pokes INFocusStatusCenter,
    /// which aborts the runner with signal 6 when it fires repeatedly across
    /// tests (each SessionManager test re-creates one). Always-false in test
    /// context is the contract that lets SessionManagerTests stay green.
    func testFocusMonitorInTestContextIsAlwaysFalseAndSafe() {
        let m1 = FocusMonitor()
        let m2 = FocusMonitor()
        XCTAssertFalse(m1.isFocusActive)
        XCTAssertFalse(m2.isFocusActive)
    }

    // MARK: - FocusMonitor.readIsFocused (#782 regression: no KVC crash on undiscoverable keys)

    /// Real `@objc dynamic` accessors stand in for `INFocusStatusCenter` /
    /// `INFocusStatus` without linking Intents.framework, so `responds(to:)`
    /// and the selector dispatch in `readIsFocused` exercise the same path
    /// they'd take against the live SDK classes. `isFocused` is typed
    /// `NSNumber`, not `Bool` — matching `INFocusStatus`'s real
    /// `NSNumber * _Nullable` ABI, since a `Bool`-typed fake would generate a
    /// raw-`BOOL` accessor and mask an object-vs-scalar return mismatch.
    @objc private class FakeFocusStatus: NSObject {
        @objc dynamic var isFocused: NSNumber
        init(isFocused: Bool) {
            self.isFocused = NSNumber(value: isFocused)
            super.init()
        }
    }

    @objc private class FakeFocusCenter: NSObject {
        @objc dynamic var focusStatus: FakeFocusStatus
        init(focusStatus: FakeFocusStatus) {
            self.focusStatus = focusStatus
            super.init()
        }
    }

    /// Exposes no `isFocused` accessor, standing in for a `focusStatus` whose
    /// key regressed out of KVC-discoverability.
    @objc private class FakeFocusStatusMissingIsFocused: NSObject {}

    @objc private class FakeFocusCenterWithBadStatus: NSObject {
        @objc dynamic var focusStatus: FakeFocusStatusMissingIsFocused
        init(focusStatus: FakeFocusStatusMissingIsFocused) {
            self.focusStatus = focusStatus
            super.init()
        }
    }

    func testReadIsFocusedReturnsTrueWhenFocused() {
        let center = FakeFocusCenter(focusStatus: FakeFocusStatus(isFocused: true))
        XCTAssertTrue(FocusMonitor.readIsFocused(center: center))
    }

    func testReadIsFocusedReturnsFalseWhenNotFocused() {
        let center = FakeFocusCenter(focusStatus: FakeFocusStatus(isFocused: false))
        XCTAssertFalse(FocusMonitor.readIsFocused(center: center))
    }

    /// Lock-in for #782: a center whose `focusStatus` key isn't KVC-discoverable
    /// must fall back to `false`, not raise `NSUnknownKeyException`.
    func testReadIsFocusedReturnsFalseWithoutCrashingWhenFocusStatusMissing() {
        let center = NSObject()
        XCTAssertFalse(FocusMonitor.readIsFocused(center: center))
    }

    /// Lock-in for #782: a `focusStatus` whose `isFocused` key isn't
    /// KVC-discoverable must also fall back to `false`, not crash.
    func testReadIsFocusedReturnsFalseWithoutCrashingWhenIsFocusedMissing() {
        let center = FakeFocusCenterWithBadStatus(focusStatus: FakeFocusStatusMissingIsFocused())
        XCTAssertFalse(FocusMonitor.readIsFocused(center: center))
    }
}
