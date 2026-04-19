import Foundation
import OSLog

/// Terminal.app activator: selects the tab whose `tty` matches the
/// session's captured controlling TTY.
///
/// Terminal.app has no session-UUID analog on tabs, but its scripting
/// dictionary exposes `tty` on tabs. Every process in a tab shares the
/// same controlling TTY, so it's a stable selector for as long as the
/// tab lives.
///
/// Uses `select` + `set index` only — no `do script`, which would type
/// into the user's live shells.
struct TerminalAppActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "TerminalAppActivator")

    let termProgram = "Apple_Terminal"
    let bundleID = "com.apple.Terminal"

    func activate(_ session: SessionState) -> Bool {
        guard let tty = session.launcher?.tty, !tty.isEmpty else {
            Self.logger.info("no tty for session \(session.id, privacy: .public)")
            return false
        }
        DispatchQueue.global(qos: .userInitiated).async {
            _ = Self.selectTab(tty: tty)
        }
        return true
    }

    private static func selectTab(tty: String) -> Bool {
        let safe = AppleScriptRunner.escape(tty)
        // `activate` LAST, after the window is already at index 1 and the
        // tab is selected — if we activate first, Terminal races to the
        // foreground while we're still reordering, and the previously
        // frontmost window shows through until the next click.
        let source = """
        tell application "Terminal"
            repeat with w in windows
                repeat with t in tabs of w
                    if tty of t is "\(safe)" then
                        set selected tab of w to t
                        set index of w to 1
                        activate
                        return "1"
                    end if
                end repeat
            end repeat
            activate
            return "0"
        end tell
        """
        let result = AppleScriptRunner.run(source, tag: "Terminal")
        let matched = result == "1"
        if !matched {
            logger.info("Terminal: no tab matched tty \(tty, privacy: .public)")
        }
        return matched
    }
}
