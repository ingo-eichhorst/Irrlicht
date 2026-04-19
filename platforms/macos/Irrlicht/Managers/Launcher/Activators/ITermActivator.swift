import Foundation
import OSLog

/// iTerm2 activator: selects the exact session (window/tab/pane) by its
/// AppleScript `unique id`, which we captured from `$ITERM_SESSION_ID`
/// at session birth.
///
/// Why AppleScript and not AX: iTerm2's window titles are just the
/// program name (`"zsh"`) — no path signal for the AX matcher. But its
/// scripting dictionary exposes `unique id` on sessions and a `select`
/// action on windows/tabs/sessions that works across spaces and screens.
/// `select` is pure focus — no `do script`, nothing typed into shells.
struct ITermActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "ITermActivator")

    let termProgram = "iTerm.app"
    let bundleID = "com.googlecode.iterm2"

    func activate(_ session: SessionState) -> Bool {
        guard let uuid = Self.iTermUUID(from: session.launcher?.itermSessionID) else {
            Self.logger.info("no iterm_session_id for session \(session.id, privacy: .public)")
            return false
        }
        DispatchQueue.global(qos: .userInitiated).async {
            _ = Self.selectSession(uuid: uuid)
        }
        return true
    }

    /// Extracts the UUID portion from an `$ITERM_SESSION_ID` value. Accepts
    /// both legacy `w0t0p0:UUID` and current `w0:t0:p0:UUID` formats by
    /// taking the substring after the *last* colon.
    static func iTermUUID(from sessionID: String?) -> String? {
        guard let sid = sessionID, !sid.isEmpty else { return nil }
        guard let r = sid.range(of: ":", options: .backwards) else { return sid }
        let tail = String(sid[r.upperBound...])
        return tail.isEmpty ? nil : tail
    }

    private static func selectSession(uuid: String) -> Bool {
        let safe = AppleScriptRunner.escape(uuid)
        let source = """
        tell application "iTerm"
            activate
            repeat with w in windows
                repeat with t in tabs of w
                    repeat with s in sessions of t
                        if (unique id of s) is "\(safe)" then
                            select w
                            tell t to select
                            tell s to select
                            return "1"
                        end if
                    end repeat
                end repeat
            end repeat
            return "0"
        end tell
        """
        let result = AppleScriptRunner.run(source, tag: "iTerm")
        let matched = result == "1"
        if !matched {
            logger.info("iTerm: no session matched uuid \(uuid, privacy: .public)")
        }
        return matched
    }
}
