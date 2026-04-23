import Foundation
import OSLog

/// Decorator that adds tmux pane selection before delegating to the host
/// terminal's activator. When a session runs inside a tmux pane the pane
/// must be selected *before* the host window is raised, so the user lands
/// in the right pane rather than wherever tmux left the cursor last time.
///
/// `SessionLauncher.jump` wraps the resolved inner activator with this
/// type when `session.launcher?.tmuxPane` is non-nil. `TmuxActivator` is
/// never stored in the registry directly.
struct TmuxActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "TmuxActivator")

    // Forwarded from the wrapped activator so bundleID(for:) and logging
    // continue to work after wrapping.
    var termProgram: String { inner.termProgram }
    var bundleID: String    { inner.bundleID }

    let inner: HostActivator

    func activate(_ session: SessionState) -> Bool {
        guard let socket = session.launcher?.tmuxSocket,
              let pane   = session.launcher?.tmuxPane,
              !socket.isEmpty, !pane.isEmpty
        else {
            Self.logger.info("tmux: missing socket or pane — delegating directly")
            return inner.activate(session)
        }

        // Select the pane first so it's active when the host terminal window
        // comes to the foreground.
        let result = ProcessRunner.run(
            "/usr/bin/env",
            args: ["tmux", "-S", socket, "select-pane", "-t", pane],
            timeout: 2.0
        )
        if result.status != 0 {
            Self.logger.info("tmux select-pane failed (status \(result.status)): \(result.stderr, privacy: .public)")
        }

        return inner.activate(session)
    }
}
