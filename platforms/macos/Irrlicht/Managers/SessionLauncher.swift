import Foundation
import OSLog

/// Brings the originating terminal or IDE window for a session to the
/// foreground. Entry point for the session-row tap gesture
/// (`SessionListView`) and the notification click handler
/// (`SessionManager`).
///
/// Dispatch is a simple registry lookup: pick the `HostActivator` whose
/// `termProgram` matches `session.launcher?.termProgram` and hand off.
/// Adding a new host = one line in `activators` (plus a new activator
/// file if it needs a strategy beyond `AXTitleMatchActivator`).
///
/// The activators themselves implement the actual AppleScript / AX
/// targeting logic — see `Managers/Launcher/Activators/`.
enum SessionLauncher {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionLauncher")

    /// Registry of supported hosts. Order is irrelevant — lookup is by
    /// `termProgram` equality, not first-match iteration.
    private static let activators: [HostActivator] = [
        ITermActivator(),
        TerminalAppActivator(),
        AXTitleMatchActivator(termProgram: "vscode",   bundleID: "com.microsoft.VSCode"),
        AXTitleMatchActivator(termProgram: "cursor",   bundleID: "com.todesktop.230313mzl4w4u92"),
        AXTitleMatchActivator(termProgram: "windsurf", bundleID: "com.exafunction.windsurf"),
        AXTitleMatchActivator(termProgram: "ghostty",  bundleID: "com.mitchellh.ghostty"),
        AXTitleMatchActivator(termProgram: "WezTerm",  bundleID: "com.github.wez.wezterm"),
        AXTitleMatchActivator(termProgram: "Hyper",    bundleID: "co.zeit.hyper"),
        AXTitleMatchActivator(termProgram: "Warp",     bundleID: "dev.warp.Warp-Stable"),
    ]

    /// Brings the session's originating terminal/IDE window to the front.
    /// Safe to call on the main thread — activators dispatch any blocking
    /// work themselves.
    static func jump(_ session: SessionState) {
        guard let tp = session.launcher?.termProgram else {
            logger.info("no launcher for session \(session.id, privacy: .public)")
            return
        }
        guard let activator = activators.first(where: { $0.termProgram == tp }) else {
            logger.info("no activator for term program \(tp, privacy: .public)")
            return
        }
        _ = activator.activate(session)
    }

    /// Returns the macOS bundle ID for a `$TERM_PROGRAM` value, or nil
    /// when none is registered. Thin wrapper over the activator registry
    /// — primarily kept for tests that verify the host map.
    static func bundleID(for termProgram: String?) -> String? {
        guard let tp = termProgram else { return nil }
        return activators.first { $0.termProgram == tp }?.bundleID
    }
}
