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
/// in Info.plist. On first launch with `.notDetermined`, we request authorization
/// — the system shows a permission dialog. After approval, `focusStatus.isFocused`
/// returns the live Focus state. Without authorization the property returns nil,
/// and we fall back to `false` (= pre-fix behavior: TTS plays under Focus).
///
/// **Dev signing caveat.** Per the same constraint that applies to notification
/// permission (see `SessionManager.requestWithTemporaryWindow`), ad-hoc-signed
/// dev builds may not get the prompt at all and silently end up `.denied`. The
/// dev workflow uses a persistent self-signed identity ("Irrlicht Dev", set up
/// by `tools/dev-sign-setup.sh`) which is stronger than ad-hoc. Released
/// Developer-ID-signed builds present the dialog correctly.
///
/// **Why not the older approach.** Revision 1 of this monitor read
/// `~/Library/Preferences/com.apple.ncprefs.plist` for `userPref.enabled` and
/// `data[].storeAssertionRecords`. Field testing on macOS Sequoia 15.7.4
/// showed *neither* schema is present — current Focus state has migrated to
/// `~/Library/DoNotDisturb/DB/Assertions.json`, which is TCC-protected and
/// requires Full Disk Access. INFocusStatusCenter is the only supported API.
final class FocusMonitor: FocusStateProviding, @unchecked Sendable {
    /// True only when running inside the real Irrlicht.app bundle. Test
    /// binaries (xctest) have a different bundle identifier and crash if we
    /// poke the Intents framework from them.
    private static let isAppContext: Bool = Bundle.main.bundleIdentifier == "io.irrlicht.app"

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
