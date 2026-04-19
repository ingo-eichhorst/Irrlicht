import AppKit
import Foundation
import OSLog

/// SessionLauncher brings the originating terminal or IDE for a session back
/// to the foreground. Used by the session-row tap gesture and the notification
/// click handler.
///
/// Dispatch order, most-specific first:
///   1. iTerm2 session by $ITERM_SESSION_ID (AppleScript, with match detection)
///   2. VS Code / Cursor / Windsurf → their file URL scheme against cwd
///   3. tmux switch-client by socket + pane, then fall through to host terminal
///   4. NSWorkspace activate by bundle ID derived from termProgram (covers Terminal.app,
///      Ghostty, Warp, WezTerm, Hyper, and iTerm2 when tier 1 found no match)
///   5. Finder-reveal of session.cwd (final fallback)
///
/// Each tier logs and falls through on failure — permission denials from
/// AppleScript must never crash the UI.
enum SessionLauncher {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionLauncher")

    /// Maps $TERM_PROGRAM values to macOS bundle identifiers for the fallback
    /// NSWorkspace activation tier. tmux is intentionally absent — a tmux
    /// passthrough has no host terminal we can activate by itself.
    private static let bundleIDByTermProgram: [String: String] = [
        "iTerm.app":      "com.googlecode.iterm2",
        "Apple_Terminal": "com.apple.Terminal",
        "vscode":         "com.microsoft.VSCode",
        "cursor":         "com.todesktop.230313mzl4w4u92",
        "windsurf":       "com.exafunction.windsurf",
        "ghostty":        "com.mitchellh.ghostty",
        "WezTerm":        "com.github.wez.wezterm",
        "Hyper":          "co.zeit.hyper",
        "Warp":           "dev.warp.Warp-Stable",
    ]

    /// Derives the macOS bundle ID for a $TERM_PROGRAM value, or nil when we
    /// don't know one. Kept client-side so the daemon's domain model stays
    /// platform-neutral.
    static func bundleID(for termProgram: String?) -> String? {
        guard let termProgram else { return nil }
        return bundleIDByTermProgram[termProgram]
    }

    /// Brings the session's originating terminal/IDE to the foreground.
    /// Safe to call on the main thread; AppleScript work is dispatched to a
    /// background queue internally.
    static func jump(_ session: SessionState) {
        DispatchQueue.global(qos: .userInitiated).async {
            dispatch(session)
        }
    }

    private static func dispatch(_ session: SessionState) {
        let launcher = session.launcher

        // 1. iTerm2 — precise per-session targeting.
        if launcher?.termProgram == "iTerm.app",
           let sid = launcher?.itermSessionID, !sid.isEmpty {
            if runAppleScriptMatching(iterm2SelectScript(sessionID: sid)) {
                return
            }
            // No matching session (window closed, session rotated): fall
            // through to tier 4 and just bring iTerm2 frontmost.
        }

        // 2. VS Code / Cursor / Windsurf — open the folder in the editor.
        if let scheme = editorURLScheme(for: launcher?.termProgram),
           !session.cwd.isEmpty,
           let url = editorFolderURL(scheme: scheme, cwd: session.cwd),
           openURL(url) {
            return
        }

        // 3. tmux — switch-client then fall through to the host terminal.
        if let pane = launcher?.tmuxPane, !pane.isEmpty {
            runTmuxSwitch(socket: launcher?.tmuxSocket, pane: pane)
            // Intentional fall-through: bring the host terminal frontmost.
        }

        // 4. NSWorkspace activate by bundle ID. Covers Terminal.app (we don't
        //    target individual tabs — Terminal.app AppleScript has no way to
        //    read a tab's env, and `do script` would type commands into real
        //    tabs), Ghostty, Warp, WezTerm, Hyper, and iTerm2 fallback.
        if let bundleID = bundleID(for: launcher?.termProgram) {
            activateApp(bundleID: bundleID)
            return
        }

        // 5. Final fallback: Finder-reveal the working directory.
        if !session.cwd.isEmpty {
            let url = URL(fileURLWithPath: session.cwd, isDirectory: true)
            NSWorkspace.shared.activateFileViewerSelecting([url])
            return
        }

        logger.info("SessionLauncher: no target for session \(session.id, privacy: .public)")
    }

    // MARK: - Pure helpers (testable)

    /// Returns the editor URL scheme for a $TERM_PROGRAM value that Irrlicht
    /// supports directly, or nil when the program isn't a folder-opening editor.
    static func editorURLScheme(for termProgram: String?) -> String? {
        switch termProgram {
        case "vscode":   return "vscode"
        case "cursor":   return "cursor"
        case "windsurf": return "windsurf"
        default:         return nil
        }
    }

    /// Builds `<scheme>://file<cwd>` using URLComponents so the path segment is
    /// correctly percent-encoded. Returns nil when the components don't form a
    /// valid URL (e.g. non-absolute cwd).
    static func editorFolderURL(scheme: String, cwd: String) -> URL? {
        var comps = URLComponents()
        comps.scheme = scheme
        comps.host = "file"
        comps.path = cwd
        return comps.url
    }

    /// Escapes a string for safe interpolation into an AppleScript double-
    /// quoted literal. Backslashes must be escaped before quotes, otherwise
    /// the quote's own escape gets re-escaped and the literal breaks.
    static func appleScriptEscape(_ s: String) -> String {
        return s
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
    }

    // MARK: - Scripts

    static func iterm2SelectScript(sessionID: String) -> String {
        // Returns "1" if a session with the given unique id was found and
        // selected, "0" otherwise — so the Swift caller can fall through to
        // a bundle-ID activate when the originating session is gone.
        let safe = appleScriptEscape(sessionID)
        return """
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
    }

    // MARK: - AppleScript execution

    /// Runs an AppleScript expected to return `"1"` on match / `"0"` on
    /// no-match. Returns true only when the script executed successfully AND
    /// returned "1". A raised AppleScript error (e.g. automation permission
    /// denied) returns false so callers can fall through to other tiers.
    private static func runAppleScriptMatching(_ source: String) -> Bool {
        var error: NSDictionary?
        guard let script = NSAppleScript(source: source) else { return false }
        let descriptor = script.executeAndReturnError(&error)
        if let error {
            logger.error("SessionLauncher AppleScript failed: \(error, privacy: .public)")
            return false
        }
        return descriptor.stringValue == "1"
    }

    // MARK: - tmux

    private static func runTmuxSwitch(socket: String?, pane: String) {
        let task = Process()
        task.launchPath = "/usr/bin/env"
        var args = ["tmux"]
        if let s = socket, !s.isEmpty {
            args += ["-S", s]
        }
        args += ["switch-client", "-t", pane]
        task.arguments = args
        do {
            try task.run()
            task.waitUntilExit()
        } catch {
            logger.error("SessionLauncher: tmux switch-client failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    // MARK: - URL + bundle activation

    private static func openURL(_ url: URL) -> Bool {
        let opened = NSWorkspace.shared.open(url)
        if !opened {
            logger.error("SessionLauncher: NSWorkspace.open failed for \(url.absoluteString, privacy: .public)")
        }
        return opened
    }

    /// Brings the app identified by bundleID to the front. Async under the
    /// hood; we never block waiting for the open. Errors are logged, never
    /// propagated — the caller has already committed to this as the chosen
    /// tier and there is no useful fallback for a missing-app case beyond
    /// the UI's own error surfacing (which we don't have).
    private static func activateApp(bundleID: String) {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            logger.info("SessionLauncher: no installed app for bundle id \(bundleID, privacy: .public)")
            return
        }
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, error in
            if let error {
                logger.error("SessionLauncher: openApplication failed: \(error.localizedDescription, privacy: .public)")
            }
        }
    }
}
