import AppKit
import Foundation
import OSLog

/// Wraps `NSWorkspace.openApplication` for the generic activation path.
/// Used by AX-based activators that need to bring an app forward before
/// raising a specific window via the Accessibility API.
///
/// iTerm2 and Terminal.app deliberately do NOT use this — their
/// AppleScript's own `activate` races with LaunchServices activation and
/// the wrong window can show through on the first click. Their activators
/// let AppleScript own activation end-to-end.
enum AppActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "AppActivator")

    /// Activates the app for `bundleID` and invokes `then` on the
    /// completion queue when LaunchServices confirms. Returns false
    /// synchronously if the bundle isn't installed; otherwise the async
    /// callback carries success/failure.
    ///
    /// `then` fires on an arbitrary queue — callers that touch UI state
    /// must dispatch to main themselves.
    @discardableResult
    static func activate(bundleID: String, then: @escaping (Bool) -> Void) -> Bool {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            logger.info("no installed app for bundle id \(bundleID, privacy: .public)")
            return false
        }
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, error in
            if let error {
                logger.error("openApplication failed: \(error.localizedDescription, privacy: .public)")
                then(false)
                return
            }
            then(true)
        }
        return true
    }
}
