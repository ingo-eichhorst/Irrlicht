import Foundation
import ServiceManagement
import os

/// Thin wrapper around `SMAppService.mainApp` for registering Irrlicht as a
/// login item. The app opts the user in on first launch (gated by the
/// `didApplyDefaultLoginItem` UserDefault) and respects later opt-outs.
enum LoginItemManager {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "LoginItemManager")

    private static let launchAtLoginKey = "launchAtLogin"
    private static let didApplyDefaultKey = "didApplyDefaultLoginItem"

    /// Called once during `applicationDidFinishLaunching`. On first ever
    /// launch (when the gate flag is still false), opt the user in by
    /// registering the app as a login item. The gate flips to true whether
    /// or not registration succeeds, so a transient signing error doesn't
    /// turn into an every-launch retry — note this also means a dev who
    /// runs an unsigned build first never gets auto-opted-in by a later
    /// signed install under the same bundle ID; not a concern for end
    /// users, who only see the signed release.
    static func applyDefaultIfNeeded() {
        let defaults = UserDefaults.standard
        guard !defaults.bool(forKey: didApplyDefaultKey) else { return }
        defaults.set(true, forKey: didApplyDefaultKey)

        guard defaults.bool(forKey: launchAtLoginKey) else { return }
        guard SMAppService.mainApp.status == .notRegistered else { return }

        do {
            try SMAppService.mainApp.register()
            logger.info("Registered as login item (first-launch default)")
        } catch {
            logger.error("First-launch register() failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    /// Bound to the Preferences toggle. Writes the user's intent to disk
    /// (the `@AppStorage` binding already does this) and pushes through to
    /// `SMAppService`. Logs failures — typically unsigned debug builds —
    /// without surfacing them in the UI.
    static func setEnabled(_ enabled: Bool) {
        do {
            if enabled {
                try SMAppService.mainApp.register()
                logger.info("Registered as login item")
            } else {
                try SMAppService.mainApp.unregister()
                logger.info("Unregistered as login item")
            }
        } catch {
            logger.error("setEnabled(\(enabled)) failed: \(error.localizedDescription, privacy: .public)")
        }
    }
}
