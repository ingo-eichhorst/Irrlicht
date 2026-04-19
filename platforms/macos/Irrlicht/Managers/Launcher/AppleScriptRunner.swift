import Foundation
import OSLog

/// Thin wrapper around `NSAppleScript` that centralises escaping, error
/// logging, and the return-value-as-string contract every activator uses.
enum AppleScriptRunner {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "AppleScriptRunner")

    /// Escapes a string for safe interpolation into an AppleScript
    /// double-quoted literal. Backslashes must be escaped before quotes
    /// — otherwise the quote's own escape gets re-escaped and the literal
    /// breaks.
    static func escape(_ s: String) -> String {
        s.replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
    }

    /// Runs `source` and returns the descriptor's string value, or nil on
    /// AppleScript error (permission denied, syntax error, target app
    /// missing). Logs under the activator's `tag` for diagnostics.
    ///
    /// Callers typically check `result == "1"` vs `"0"` — scripts should
    /// return those literals to signal match/no-match so permission
    /// failures don't silently look like successes.
    static func run(_ source: String, tag: String) -> String? {
        var err: NSDictionary?
        guard let script = NSAppleScript(source: source) else {
            logger.error("\(tag, privacy: .public): NSAppleScript init failed")
            return nil
        }
        let descriptor = script.executeAndReturnError(&err)
        if let err {
            logger.error("\(tag, privacy: .public) AppleScript failed: \(err, privacy: .public)")
            return nil
        }
        return descriptor.stringValue
    }
}
