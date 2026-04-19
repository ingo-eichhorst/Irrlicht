import AppKit
import ApplicationServices
import Foundation
import OSLog

/// Brings the originating terminal or IDE window for a session to the
/// foreground. Used by the session-row tap gesture and the notification
/// click handler.
///
/// Dispatch:
///   1. `NSWorkspace.openApplication` — activate the host app.
///   2. iTerm2 only: AppleScript `select` by session UUID — works across
///      screens/spaces and uses a stable ID, not title guesswork.
///   3. Everything else: Accessibility API, raise the window whose title's
///      deepest matching ancestor segment of cwd wins. Silently no-ops if
///      AX permission isn't granted.
///
/// It works (right window) or it degrades to app activation (right app, last
/// used window). No Finder-reveal, no URL schemes that would clobber a
/// worktree's existing editor window.
enum SessionLauncher {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionLauncher")

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

    static func bundleID(for termProgram: String?) -> String? {
        guard let termProgram else { return nil }
        return bundleIDByTermProgram[termProgram]
    }

    static func jump(_ session: SessionState) {
        guard let bundleID = bundleID(for: session.launcher?.termProgram) else {
            logger.info("no launcher for session \(session.id, privacy: .public)")
            return
        }
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            logger.info("no installed app for bundle id \(bundleID, privacy: .public)")
            return
        }
        let launcher = session.launcher
        let cwd = session.cwd
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, error in
            if let error {
                logger.error("openApplication failed: \(error.localizedDescription, privacy: .public)")
                return
            }
            // iTerm2 fast path: select the exact session by UUID — only
            // reliable cross-space/screen target we have for iTerm.
            if launcher?.termProgram == "iTerm.app",
               let uuid = iTermUUID(from: launcher?.itermSessionID),
               selectITermSession(uuid: uuid) {
                return
            }
            // Everything else: AX + cwd-ancestor title match.
            if !cwd.isEmpty {
                raiseMatchingWindow(bundleID: bundleID, cwd: cwd)
            }
        }
    }

    // MARK: - iTerm2 AppleScript

    /// Extracts the UUID portion from an `$ITERM_SESSION_ID` value. Accepts
    /// both legacy `w0t0p0:UUID` and current `w0:t0:p0:UUID` formats by
    /// taking the substring after the *last* colon.
    static func iTermUUID(from sessionID: String?) -> String? {
        guard let sid = sessionID, !sid.isEmpty else { return nil }
        guard let r = sid.range(of: ":", options: .backwards) else { return sid }
        let tail = String(sid[r.upperBound...])
        return tail.isEmpty ? nil : tail
    }

    /// Runs iTerm2 AppleScript to `select` the session with the given
    /// `unique id`. Returns true on a real match, false on AppleScript
    /// error (permission denied) or no-match (window/session closed).
    private static func selectITermSession(uuid: String) -> Bool {
        let safe = uuid
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
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
        var err: NSDictionary?
        guard let script = NSAppleScript(source: source) else { return false }
        let descriptor = script.executeAndReturnError(&err)
        if let err {
            logger.error("iTerm AppleScript failed: \(err, privacy: .public)")
            return false
        }
        let matched = descriptor.stringValue == "1"
        if !matched {
            logger.info("iTerm AppleScript: no session matched uuid \(uuid, privacy: .public)")
        }
        return matched
    }

    // MARK: - AX window selection

    private static func raiseMatchingWindow(bundleID: String, cwd: String) {
        guard ensureAXTrust() else {
            logger.info("AX permission not granted — staying on app-level activation")
            return
        }
        let runningApps = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
        guard let app = runningApps.first else { return }
        let axApp = AXUIElementCreateApplication(app.processIdentifier)

        var windowsRef: CFTypeRef?
        let status = AXUIElementCopyAttributeValue(axApp, kAXWindowsAttribute as CFString, &windowsRef)
        guard status == .success, let windows = windowsRef as? [AXUIElement] else { return }

        let titles = windows.map { windowTitle($0) }
        guard let idx = bestMatchIndex(titles: titles, cwd: cwd) else {
            logger.info("no window title matched cwd \(cwd, privacy: .public); candidates=\(titles, privacy: .public)")
            return
        }
        let target = windows[idx]
        AXUIElementPerformAction(target, kAXRaiseAction as CFString)
        AXUIElementSetAttributeValue(target, kAXMainAttribute as CFString, kCFBooleanTrue)
    }

    private static func windowTitle(_ window: AXUIElement) -> String {
        var titleRef: CFTypeRef?
        AXUIElementCopyAttributeValue(window, kAXTitleAttribute as CFString, &titleRef)
        return (titleRef as? String) ?? ""
    }

    private static func ensureAXTrust() -> Bool {
        let key = kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String
        let opts = [key: true] as CFDictionary
        return AXIsProcessTrustedWithOptions(opts)
    }

    // MARK: - Title match (pure, testable)

    /// Generic root-level path segments that must never serve as a match
    /// signal — they appear in virtually every home-directory path string.
    private static let genericTopSegments: Set<String> = [
        "Users", "home", "tmp", "var", "private", "opt", "mnt", "root"
    ]

    /// Scores a window title against a cwd. Higher score = better match.
    /// Returns 0 when the title shares no meaningful path segment with cwd.
    ///
    /// - Tier A (score 1000): title contains the full absolute cwd — common
    ///   for terminal tab titles, and the only signal that should beat an
    ///   ancestor match regardless of depth.
    /// - Tier B (score = depth index + 1): deepest path segment of cwd that
    ///   appears in the title wins. This handles VS Code, whose window title
    ///   shows only the *workspace folder* name (e.g. `"2.1.114 — irrlicht"`)
    ///   even when the Claude Code session's cwd is a subdirectory several
    ///   levels below (`.../irrlicht/.claude/worktrees/170`). The deeper the
    ///   matching ancestor, the more specific the signal.
    ///
    /// Generic top segments (`Users`, user home basename, `tmp`, etc.) are
    /// skipped because they occur in nearly every string and would cause
    /// false matches.
    static func titleMatchScore(title: String, cwd: String) -> Int {
        if title.isEmpty || cwd.isEmpty { return 0 }
        if title.contains(cwd) { return 1_000 }

        let trimmed = cwd.hasSuffix("/") ? String(cwd.dropLast()) : cwd
        let parts = trimmed.split(separator: "/", omittingEmptySubsequences: true).map(String.init)

        let homeBasename = (ProcessInfo.processInfo.environment["HOME"] ?? "")
            .split(separator: "/").last.map(String.init) ?? ""

        for i in (0..<parts.count).reversed() {
            let p = parts[i]
            if p.isEmpty { continue }
            if genericTopSegments.contains(p) { continue }
            if !homeBasename.isEmpty && p == homeBasename { continue }
            if title.contains(p) { return i + 1 }
        }
        return 0
    }

    /// Index of the highest-scoring title, or nil when all scores are 0.
    /// Ties break by first occurrence (AX returns windows in z-order; the
    /// topmost matching window wins).
    static func bestMatchIndex(titles: [String], cwd: String) -> Int? {
        var best: (idx: Int, score: Int)?
        for (i, t) in titles.enumerated() {
            let s = titleMatchScore(title: t, cwd: cwd)
            if s == 0 { continue }
            if best == nil || s > best!.score {
                best = (i, s)
            }
        }
        return best?.idx
    }
}
