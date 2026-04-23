import AppKit
import Foundation
import OSLog

/// Activator for kitty terminal. Uses kitty's remote-control socket API
/// (`kitten @ focus-window`) for precise window targeting — no AX needed.
///
/// Kitty does not set `$TERM_PROGRAM` by default (intentional; see upstream
/// issue #4793). Detection relies on `$KITTY_LISTEN_ON` (the remote-control
/// socket) and `$KITTY_WINDOW_ID`, both now whitelisted in the Go detector.
/// When the socket is available, focus is exact; otherwise we fall back to
/// app-level NSWorkspace activation.
struct KittyActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "KittyActivator")

    let termProgram = "kitty"
    let bundleID    = "net.kovidgoyal.kitty"

    func activate(_ session: SessionState) -> Bool {
        // Prefer socket-based remote control when KITTY_LISTEN_ON is present.
        if let socket = session.launcher?.kittyListenOn, !socket.isEmpty,
           let windowID = session.launcher?.kittyWindowID, !windowID.isEmpty {
            // Dispatch to background: ProcessRunner.run blocks up to `timeout`
            // seconds and must not freeze the main run loop.
            DispatchQueue.global(qos: .userInitiated).async {
                self.focusViaSocket(socket: socket, windowID: windowID)
            }
            return true
        }
        // Fallback: app-level activation (no tab/window precision).
        Self.logger.info("kitty: no socket/windowID — falling back to app-level activation")
        return AppActivator.activate(bundleID: bundleID) { _ in }
    }

    @discardableResult
    private func focusViaSocket(socket: String, windowID: String) -> Bool {
        // $KITTY_LISTEN_ON is in the form "unix:/path/to/socket" or "tcp:host:port".
        // `kitten @` accepts the value directly via --to.
        let kitten = findKitten()
        guard let kitten else {
            Self.logger.info("kitten not found; falling back to app-level activation")
            return AppActivator.activate(bundleID: bundleID) { _ in }
        }
        let result = ProcessRunner.run(kitten,
            args: ["@", "--to", socket, "focus-window", "--match", "id:\(windowID)"],
            timeout: 3.0)
        if result.status == 0 {
            // Also bring the app to the foreground via NSWorkspace so the OS
            // registers it as the active app (kitten moves the kitty-internal
            // focus but doesn't always call NSApplicationActivate).
            _ = AppActivator.activate(bundleID: bundleID) { _ in }
            return true
        }
        Self.logger.info("kitten @ focus-window failed (status \(result.status)): \(result.stderr, privacy: .public)")
        return false
    }

    /// Resolves the `kitten` binary. Tries the kitty app bundle first, then
    /// the default Homebrew / user-local paths.
    private func findKitten() -> String? {
        let candidates = [
            "/Applications/kitty.app/Contents/MacOS/kitten",
            "/usr/local/bin/kitten",
            "/opt/homebrew/bin/kitten",
            (ProcessInfo.processInfo.environment["HOME"] ?? "") + "/.local/bin/kitten",
        ]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }
}
