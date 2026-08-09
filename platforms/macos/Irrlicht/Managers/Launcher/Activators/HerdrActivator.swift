import Foundation
import OSLog

/// Decorator that selects the session's herdr pane, then delegates to the
/// activator for the terminal that displays it.
///
/// herdr is a persistent multiplexer: its server owns every pane's pty,
/// outlives any attached client and is reparented to init. So the window a
/// pane is shown in belongs to a *client* process, not to the pane's own
/// process tree — the daemon resolves that client and reports its identity in
/// the launcher's ordinary host fields, which is what `inner` was built from
/// (issue #1350). A session with no attached client resolves to no host at all
/// and never reaches this type: there is genuinely no window to raise.
///
/// `SessionLauncher.resolveActivator` wraps the resolved inner activator with
/// this type when `session.launcher?.herdrPaneID` is set. `HerdrActivator` is
/// never stored in the registry directly.
struct HerdrActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "HerdrActivator")

    // Forwarded from the wrapped activator so bundleID(for:) and logging
    // continue to work after wrapping.
    var termProgram: String { inner.termProgram }
    var bundleID: String    { inner.bundleID }

    let inner: HostActivator

    func activate(_ session: SessionState) -> Bool {
        guard let pane = session.launcher?.herdrPaneID,
              let socket = session.launcher?.herdrSocketPath,
              !pane.isEmpty, !socket.isEmpty
        else {
            // Both halves are required: a pane address like "w1:p1" exists on
            // every running herdr server, so without the socket the focus
            // could land in an unrelated session. Mirrors the daemon's
            // resolveBackend guard.
            Self.logger.info("herdr: missing pane or socket — delegating directly")
            return inner.activate(session)
        }

        // Raise the window FIRST, synchronously, inside the click handler's
        // user-attention window: macOS denies a cross-app activation issued
        // after async work and lets the previous frontmost app reclaim focus
        // (the "raise then drop back" symptom documented on KittyActivator).
        // Pane selection follows async — herdr redraws the pane in place, so
        // ordering it second costs a redraw, not a wrong window. This is the
        // one place this activator deliberately differs from TmuxActivator,
        // which runs its selection before delegating.
        //
        // The guarantee is only as synchronous as `inner`. When the herdr
        // client is itself inside tmux, `inner` is TmuxActivator, which returns
        // immediately and does its own raise on a background queue — so that
        // composed path inherits tmux's existing async raise and this ordering
        // buys nothing. Fixing it means changing TmuxActivator's ordering for
        // every tmux user, which is out of scope here; herdr-in-tmux is the
        // rare nesting, and the plain path is the one this protects.
        let activated = inner.activate(session)

        DispatchQueue.global(qos: .userInitiated).async {
            Self.focusPane(pane: pane, socket: socket)
        }
        return activated
    }

    /// Runs `herdr agent focus <pane>` against the server at `socket`.
    ///
    /// Note the verb: `herdr pane focus` is *directional* ("focus a
    /// neighbouring pane") and would move the user somewhere else entirely.
    /// Addressing rides in the environment because herdr's CLI has no socket
    /// flag. Exit status is meaningful — an unknown pane exits 1 (verified
    /// against herdr 0.8.0).
    private static func focusPane(pane: String, socket: String) {
        guard let herdr = herdrPath else {
            logger.info("herdr binary not found; pane will not be selected")
            return
        }
        let result = ProcessRunner.run(
            herdr,
            args: ["agent", "focus", pane],
            env: ["HERDR_SOCKET_PATH": socket],
            timeout: 3.0
        )
        if result.status != 0 {
            logger.info("herdr agent focus failed (status \(result.status)): \(result.stderr, privacy: .public)")
        }
    }

    /// Absolute path to the `herdr` binary, or nil when it isn't installed.
    /// Resolved from a candidate list rather than via `/usr/bin/env` because a
    /// GUI app's PATH is LaunchServices', not the user's login shell's — and
    /// herdr's own installer puts it in ~/.local/bin. Same approach as
    /// `KittyActivator.kittenPath`.
    private static let herdrPath: String? = {
        let home = ProcessInfo.processInfo.environment["HOME"] ?? ""
        let candidates = [
            home + "/.local/bin/herdr",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
            "/opt/homebrew/bin/herdr",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
            "/usr/local/bin/herdr",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
        ]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }()
}
