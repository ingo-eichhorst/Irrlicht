import Foundation
import OSLog

/// Injects input / interrupts into a session whose terminal backend the daemon
/// can't script directly — iTerm2 and Terminal.app, reached via AppleScript
/// (the app holds the Automation TCC grant the daemon lacks). Triggered by a
/// PushTypeInputRequested message (issue #724). tmux/kitty are handled
/// daemon-side and never reach here.
///
/// Unlike the Focus activators, this DOES type into the user's shell — that is
/// the point. It only runs for sessions the user made controllable (backchannel
/// toggle on + `control` consent granted), enforced daemon-side before the push.
enum SessionInputActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionInputActivator")

    static func inject(_ session: SessionState, action: String, data: String) {
        guard let launcher = session.launcher else { return }
        DispatchQueue.global(qos: .userInitiated).async {
            switch launcher.termProgram {
            case "iTerm.app":
                guard let uuid = ITermActivator.iTermUUID(from: launcher.itermSessionID) else { return }
                iterm(uuid: uuid, action: action, data: data)
            case "Apple_Terminal":
                guard let tty = launcher.tty, !tty.isEmpty else { return }
                terminal(tty: tty, action: action, data: data)
            default:
                logger.info("input_requested for unsupported host \(launcher.termProgram ?? "nil", privacy: .public)")
            }
        }
    }

    private static func iterm(uuid: String, action: String, data: String) {
        let safeUUID = AppleScriptRunner.escape(uuid)
        let command: String
        if action == "interrupt" {
            command = "tell s to write text (character id 3) without newline"
        } else {
            // `write text` appends a return (submits); strip a trailing newline
            // from the payload so we don't double-submit.
            command = "tell s to write text \"\(AppleScriptRunner.escape(stripTrailingNewline(data)))\""
        }
        let source = """
        tell application "iTerm"
            repeat with w in windows
                repeat with t in tabs of w
                    repeat with s in sessions of t
                        if (unique id of s) is "\(safeUUID)" then
                            \(command)
                            return "1"
                        end if
                    end repeat
                end repeat
            end repeat
            return "0"
        end tell
        """
        if AppleScriptRunner.run(source, tag: "iTermInput") != "1" {
            logger.info("iTerm input: no session matched uuid \(uuid, privacy: .public)")
        }
    }

    private static func terminal(tty: String, action: String, data: String) {
        let safeTTY = AppleScriptRunner.escape(tty)
        let payload = action == "interrupt"
            ? "(character id 3)"
            : "\"\(AppleScriptRunner.escape(stripTrailingNewline(data)))\""
        let source = """
        tell application "Terminal"
            repeat with w in windows
                repeat with t in tabs of w
                    if tty of t is "\(safeTTY)" then
                        do script \(payload) in t
                        return "1"
                    end if
                end repeat
            end repeat
            return "0"
        end tell
        """
        if AppleScriptRunner.run(source, tag: "TerminalInput") != "1" {
            logger.info("Terminal input: no tab matched tty \(tty, privacy: .public)")
        }
    }

    private static func stripTrailingNewline(_ s: String) -> String {
        var out = s
        while out.hasSuffix("\r") || out.hasSuffix("\n") { out.removeLast() }
        return out
    }
}
