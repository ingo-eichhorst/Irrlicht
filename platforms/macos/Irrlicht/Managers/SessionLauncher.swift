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
        AXTitleMatchActivator(termProgram: "vscode",    bundleID: "com.microsoft.VSCode"),
        AXTitleMatchActivator(termProgram: "cursor",    bundleID: "com.todesktop.230313mzl4w4u92"),
        AXTitleMatchActivator(termProgram: "windsurf",  bundleID: "com.exafunction.windsurf"),
        AXTitleMatchActivator(termProgram: "ghostty",   bundleID: "com.mitchellh.ghostty"),
        AXTitleMatchActivator(termProgram: "WezTerm",   bundleID: "com.github.wez.wezterm"),
        AXTitleMatchActivator(termProgram: "Hyper",     bundleID: "co.zeit.hyper"),
        AXTitleMatchActivator(termProgram: "Warp",      bundleID: "dev.warp.Warp-Stable"),
        // JetBrains IDEs — fan-out activator resolves which IDE is running.
        JetBrainsActivator(),
        // Additional terminals and IDEs
        AXTitleMatchActivator(termProgram: "zed",       bundleID: "dev.zed.Zed"),
        KittyActivator(),
        AXTitleMatchActivator(termProgram: "rio",       bundleID: "com.raphaelamorim.rio"),
        AXTitleMatchActivator(termProgram: "tabby",     bundleID: "org.tabby"),
        AXTitleMatchActivator(termProgram: "waveterm",  bundleID: "dev.commandline.waveterm"),
        AXTitleMatchActivator(termProgram: "alacritty", bundleID: "org.alacritty"),
        AXTitleMatchActivator(termProgram: "nova",      bundleID: "com.panic.Nova"),
        AXTitleMatchActivator(termProgram: "cmux",      bundleID: "com.cmuxterm.app"),
    ]

    /// Brings the session's originating terminal/IDE window to the front.
    /// Safe to call on the main thread — activators dispatch any blocking
    /// work themselves.
    static func jump(_ session: SessionState) {
        guard let tp = session.launcher?.termProgram else {
            logger.info("no launcher for session \(session.id, privacy: .public)")
            return
        }
        guard let base = activators.first(where: { $0.termProgram == tp }) else {
            logger.info("no activator for term program \(tp, privacy: .public)")
            return
        }
        // Wrap with TmuxActivator when the session lives inside a tmux pane
        // so the correct pane is selected before the host window is raised.
        let activator: HostActivator = session.launcher?.tmuxPane != nil
            ? TmuxActivator(inner: base)
            : base
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
