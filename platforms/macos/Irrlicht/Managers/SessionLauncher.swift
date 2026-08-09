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
        guard let activator = resolveActivator(for: session.launcher) else {
            let tp = session.launcher?.termProgram ?? "nil"
            let bid = session.launcher?.hostBundleID ?? "nil"
            let herdr = herdrDiagnostic(for: session.launcher)
            logger.info("no activator for session \(session.id, privacy: .public) (term_program=\(tp, privacy: .public), host_bundle_id=\(bid, privacy: .public), \(herdr, privacy: .public))")
            return
        }
        _ = activator.activate(session)
    }

    /// Explains, for the "no activator" log, why a herdr session produced no
    /// window. A pane with no host at all is the expected "nothing is
    /// displaying this session" case rather than a capture failure, and saying
    /// so is what makes a detached multiplexer session distinguishable from a
    /// launcher we simply failed to read (#1350). It only claims that when the
    /// host fields really are empty: a client that *was* resolved, whose
    /// terminal has no registry entry, lands here too, and pointing that at a
    /// missing client would send whoever debugs it looking for the wrong thing.
    ///
    /// Exposed for tests, which is the only way either wording is checked.
    static func herdrDiagnostic(for launcher: Launcher?) -> String {
        guard let pane = launcher?.herdrPaneID else { return "herdr_pane=nil" }
        let unresolved = (launcher?.termProgram ?? "").isEmpty
            && (launcher?.hostBundleID ?? "").isEmpty
        let why = unresolved ? "no attached client" : "client resolved but its host has no activator"
        return "herdr_pane=\(pane), \(why)"
    }

    /// Selects the activator for a launcher without performing activation
    /// (exposed for tests). A registered activator keyed by `term_program`
    /// wins; otherwise a generic `AXTitleMatchActivator` built from the
    /// daemon-resolved host bundle id handles embedded-terminal hosts that
    /// have no registry entry (e.g. a terminal inside Obsidian) — bringing the
    /// host app/window to the front, not a specific pane. The result is wrapped
    /// in `TmuxActivator` when the session lives in a tmux pane so the correct
    /// pane is selected before the host window is raised, and in
    /// `HerdrActivator` when it lives in a herdr pane. The two compose: a herdr
    /// client can itself be running inside tmux, in which case both selections
    /// are needed and neither substitutes for the other.
    ///
    /// For a herdr session the host fields were resolved from the attached
    /// herdr client, not from the agent's own process tree (#1350) — so a
    /// detached session has no host, falls through to nil, and honestly does
    /// nothing rather than raising some other window.
    static func resolveActivator(for launcher: Launcher?) -> HostActivator? {
        var base: HostActivator?
        if let tp = launcher?.termProgram {
            base = activators.first(where: { $0.termProgram == tp })
        }
        if base == nil, let bundleID = launcher?.hostBundleID, !bundleID.isEmpty {
            base = AXTitleMatchActivator(termProgram: launcher?.termProgram ?? "", bundleID: bundleID)
        }
        guard var activator: HostActivator = base else { return nil }
        if launcher?.tmuxPane != nil {
            activator = TmuxActivator(inner: activator)
        }
        if launcher?.herdrPaneID != nil {
            activator = HerdrActivator(inner: activator)
        }
        return activator
    }

    /// Returns the macOS bundle ID for a `$TERM_PROGRAM` value, or nil
    /// when none is registered. Thin wrapper over the activator registry
    /// — primarily kept for tests that verify the host map.
    static func bundleID(for termProgram: String?) -> String? {
        guard let tp = termProgram else { return nil }
        return activators.first { $0.termProgram == tp }?.bundleID
    }
}
