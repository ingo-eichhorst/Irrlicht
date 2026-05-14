import Foundation
import Intents

/// Provides the current macOS Focus / Do Not Disturb state so notification
/// emitters can suppress sound (and TTS) alongside the system-suppressed
/// banner. Conforms to `Sendable` because the `UNUserNotificationCenter`
/// delegate's `willPresent` runs nonisolated.
protocol FocusStateProviding: AnyObject, Sendable {
    var isFocusActive: Bool { get }
}

/// Reads Focus state via `INFocusStatusCenter` (Intents framework, macOS 12+).
///
/// **Authorization model.** Requires the `com.apple.developer.focus-status`
/// entitlement (set in `Irrlicht.entitlements`) plus `NSFocusStatusUsageDescription`
/// in Info.plist. On first launch with `.notDetermined`, we call
/// `requestAuthorization`. In a properly Developer-ID-signed build that surfaces
/// a system prompt; the user grants → `focusStatus.isFocused` returns the live
/// Focus state.
///
/// **Dev signing caveat (observed on macOS Sequoia 15.7.4).** Self-signed and
/// ad-hoc builds claiming `com.apple.developer.focus-status` won't launch at
/// all (launchd refuses the entitlement). Self-signed builds *without* the
/// entitlement still load the Intents framework: `requestAuthorization` reports
/// `.authorized` (raw 3) with no prompt — but `focusStatus.isFocused` then
/// *always returns `Optional(false)`* regardless of the actual system Focus
/// state. The `auth=.authorized` reading is a misleading no-op; Apple gates the
/// real read on Developer-ID-signed binaries. Practical consequence: live Focus
/// suppression is only verifiable in the released DMG, not via `/ir:test-mac`.
/// The `?? false` fallback in `isFocusActive` therefore covers both the truly-
/// unauthorized case and the silently-faked-authorized case.
///
/// **Why not the older approach.** Revision 1 of this monitor read
/// `~/Library/Preferences/com.apple.ncprefs.plist` for `userPref.enabled` and
/// `data[].storeAssertionRecords`. Field testing on macOS Sequoia 15.7.4
/// showed *neither* schema is present — current Focus state has migrated to
/// `~/Library/DoNotDisturb/DB/Assertions.json`, which is TCC-protected and
/// requires Full Disk Access. INFocusStatusCenter is the only supported API.
final class FocusMonitor: FocusStateProviding, @unchecked Sendable {
    /// True when running as a real .app (production or dev bundle); false in
    /// xctest. The xctest host has no `.app` suffix on its bundle path, so we
    /// use that to skip Intents-framework calls — those crashed the test
    /// runner with signal 6 during SessionManager setUp when `FocusMonitor()`
    /// was constructed back-to-back across tests.
    private static let isAppContext: Bool = Bundle.main.bundlePath.hasSuffix(".app")

    init() {
        guard Self.isAppContext else {
            print("🌙 FocusMonitor: non-app context, isFocusActive will return false")
            return
        }

        let status = INFocusStatusCenter.default.authorizationStatus
        let focused = INFocusStatusCenter.default.focusStatus.isFocused ?? false
        print("🌙 FocusMonitor init: auth=\(status.rawValue) focused=\(focused)")

        switch status {
        case .notDetermined:
            INFocusStatusCenter.default.requestAuthorization { granted in
                print("🌙 FocusMonitor authorization → \(granted.rawValue)")
            }
        case .denied, .restricted:
            print("ℹ️ Focus permission unavailable — sound/TTS won't be suppressed under Focus. " +
                  "Grant in System Settings → Privacy & Security → Focus → Irrlicht.")
        case .authorized:
            break
        @unknown default:
            break
        }
    }

    /// Synchronous live read. `INFocusStatusCenter.default.focusStatus` is
    /// process-safe and cheap; no caching needed for the rate at which we
    /// emit notifications. Returns `false` in non-app contexts (xctest) so
    /// tests don't inadvertently exercise the Intents framework.
    var isFocusActive: Bool {
        guard Self.isAppContext else { return false }
        return INFocusStatusCenter.default.focusStatus.isFocused ?? false
    }
}
