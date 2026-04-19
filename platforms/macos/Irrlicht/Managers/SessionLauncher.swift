import AppKit
import Foundation
import OSLog

/// Brings the originating terminal or IDE for a session to the foreground.
/// Used by the session-row tap gesture and the notification click handler.
///
/// It works, or it doesn't. No fallback — a folder-open in Finder or a
/// new editor window is worse than no-op (a new VS Code window on the
/// same workspace kills the existing one, which kills any sessions
/// running in it, e.g. worktrees).
enum SessionLauncher {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionLauncher")

    private static let bundleIDByTermProgram: [String: String] = [
        "iTerm.app":      "com.googlecode.iterm2",
        "Apple_Terminal": "com.apple.Terminal",
        "vscode":         "com.microsoft.VSCode",
        "cursor":         "com.todesktop.230313mzl4w4u92",
        "windsurf":       "com.exafunction.windsurf",
        "ghostty":        "com.mitchellh.ghostty",
        "WezTerm":        "com.github.wez.wezterm",
        "Hyper":          "co.zeit.hyper",
        "Warp":           "dev.warp.Warp-Stable",
    ]

    static func bundleID(for termProgram: String?) -> String? {
        guard let termProgram else { return nil }
        return bundleIDByTermProgram[termProgram]
    }

    static func jump(_ session: SessionState) {
        guard let bundleID = bundleID(for: session.launcher?.termProgram) else {
            logger.info("no launcher for session \(session.id, privacy: .public)")
            return
        }
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            logger.info("no installed app for bundle id \(bundleID, privacy: .public)")
            return
        }
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, error in
            if let error {
                logger.error("openApplication failed: \(error.localizedDescription, privacy: .public)")
            }
        }
    }
}
