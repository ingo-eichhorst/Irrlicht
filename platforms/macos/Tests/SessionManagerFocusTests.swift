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
}
