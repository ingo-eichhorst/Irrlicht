import AppKit
import Foundation
import OSLog

/// Activator for kitty terminal.
///
/// Kitty does not set `$TERM_PROGRAM` by default (upstream issue #4793). The
/// Go detector whitelists `$KITTY_PID`, `$KITTY_LISTEN_ON`, and
/// `$KITTY_WINDOW_ID`; with all three set, the activator can both target the
/// specific kitty process and switch to the right tab. With only KITTY_PID,
/// it brings the right kitty instance forward but stays on the last-focused
/// tab. With none of them, it falls back to bundle activation which may pick
/// any running kitty.
///
/// Activation order matters: the OS-level activate must fire synchronously,
/// inside the click handler's user-attention window. Calling it after async
/// kitten IPC means macOS treats the request as unattended and either denies
/// it or lets the previous frontmost app reclaim focus — visible as kitty
/// briefly raising and then dropping back to the background.
struct KittyActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "KittyActivator")

    let termProgram = "kitty"
    let bundleID    = "net.kovidgoyal.kitty"

    func activate(_ session: SessionState) -> Bool {
        // 1. Activate the specific kitty process FIRST, synchronously. This
        //    runs in the click handler's foreground context where Irrlicht is
        //    still the user-attention app — macOS honors the cross-app
        //    activation. Targeting by PID also avoids picking the wrong kitty
        //    when multiple instances are running.
        let activated: Bool
        if let pid = session.launcher?.kittyPID, pid > 0,
           let app = NSRunningApplication(processIdentifier: pid_t(pid)) {
            activated = app.activate(options: [])
        } else {
            // Either KITTY_PID wasn't captured (env unreadable, e.g. pi
            // sessions) or the captured PID has exited. Fall back to bundle
            // activation — may pick the wrong instance when multiple kitties
            // run, but it's the best we can do.
            Self.logger.info("kitty: KITTY_PID unavailable for session \(session.id, privacy: .public); using bundle activation")
            // `activated` already carries the synchronous result; the async
            // `then` completion (fires once a cold-launched app finishes
            // opening) isn't needed here.
            activated = AppActivator.activate(bundleID: bundleID) { _ in }
        }

        // 2. Switch to the right tab via kitty's remote control if the user
        //    enabled it. Runs async because ProcessRunner.run can take up to
        //    `timeout` seconds. No post-kitten re-activation — step 1 already
        //    brought the right kitty forward, and any second activate-after-
        //    async would race the menu-bar-close and look like "raise then
        //    drop back".
        if let socket = session.launcher?.kittyListenOn, !socket.isEmpty,
           let windowID = session.launcher?.kittyWindowID, !windowID.isEmpty {
            DispatchQueue.global(qos: .userInitiated).async {
                Self.focusTabViaSocket(socket: socket, windowID: windowID)
            }
        } else {
            Self.logger.info("kitty: KITTY_LISTEN_ON not set — tab will not switch. See docs to enable allow_remote_control + listen_on in kitty.conf.")
        }

        return activated
    }

    private static func focusTabViaSocket(socket: String, windowID: String) {
        guard let kitten = kittenPath else {
            logger.info("kitten not found; tab will not switch")
            return
        }
        let result = ProcessRunner.run(kitten,
            args: ["@", "--to", socket, "focus-window", "--match", "id:\(windowID)"],
            timeout: 3.0)
        if result.status != 0 {
            logger.info("kitten @ focus-window failed (status \(result.status)): \(result.stderr, privacy: .public)")
        }
    }

    private static let kittenPath: String? = {
        let home = ProcessInfo.processInfo.environment["HOME"] ?? ""
        let candidates = [
            "/Applications/kitty.app/Contents/MacOS/kitten",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
            "/usr/local/bin/kitten",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
            "/opt/homebrew/bin/kitten",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
            home + "/.local/bin/kitten",  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
        ]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }()
}
