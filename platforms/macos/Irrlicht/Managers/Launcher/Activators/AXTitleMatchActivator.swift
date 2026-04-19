import AppKit
import ApplicationServices
import Foundation
import OSLog

/// Generic activator for hosts that don't expose a scripting-dictionary
/// session ID: activates the app via LaunchServices, then uses the
/// Accessibility API to raise the window whose title best matches the
/// session's cwd (deepest common ancestor segment wins).
///
/// Covers VS Code / Cursor / Windsurf (whose window titles show the
/// workspace folder name like `"README.md — irrlicht"`) and every other
/// terminal without a stable per-tab selector. Parameterised, so a new
/// host is just one line in the registry.
struct AXTitleMatchActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "AXTitleMatchActivator")

    let termProgram: String
    let bundleID: String

    func activate(_ session: SessionState) -> Bool {
        let cwd = session.cwd
        guard !cwd.isEmpty else {
            Self.logger.info("no cwd for session \(session.id, privacy: .public)")
            return false
        }
        let bid = bundleID
        let ok = AppActivator.activate(bundleID: bid) { activated in
            guard activated else { return }
            Self.raiseMatchingWindow(bundleID: bid, cwd: cwd)
        }
        return ok
    }

    // MARK: - AX window selection

    private static func raiseMatchingWindow(bundleID: String, cwd: String) {
        guard AccessibilityPermission.ensureTrusted() else {
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
