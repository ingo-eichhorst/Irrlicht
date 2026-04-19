import AppKit
import ApplicationServices
import Foundation
import OSLog

/// Brings the originating terminal or IDE window for a session to the
/// foreground. Used by the session-row tap gesture and the notification
/// click handler.
///
/// Two-step dispatch:
///   1. `NSWorkspace.openApplication` — activate the host app (no permission).
///   2. Accessibility API — raise the specific window whose title matches
///      the session's cwd. Silently no-ops if AX permission isn't granted.
///
/// It works (right window) or it degrades to app activation (right app, last
/// used window). No Finder-reveal, no URL schemes that would open a new
/// editor window and clobber the worktree's existing window.
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
        let cwd = session.cwd
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: config) { _, error in
            if let error {
                logger.error("openApplication failed: \(error.localizedDescription, privacy: .public)")
                return
            }
            if !cwd.isEmpty {
                raiseMatchingWindow(bundleID: bundleID, cwd: cwd)
            }
        }
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
            logger.info("no window title matched cwd \(cwd, privacy: .public)")
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

    /// Scores a window title against a cwd. Higher score = better match.
    /// 0 means no match — caller should skip this window.
    ///
    ///   3 — title contains the full absolute cwd (iTerm2/Terminal tab titles often do)
    ///   2 — title contains the last two cwd components joined by "/"
    ///       (disambiguates common basenames like "src" or "170")
    ///   1 — title contains just the cwd basename
    ///   0 — no match
    static func titleMatchScore(title: String, cwd: String) -> Int {
        if title.isEmpty || cwd.isEmpty { return 0 }
        if title.contains(cwd) { return 3 }
        let trimmed = cwd.hasSuffix("/") ? String(cwd.dropLast()) : cwd
        let parts = trimmed.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        if parts.count >= 2 {
            let lastTwo = parts.suffix(2).joined(separator: "/")
            if title.contains(lastTwo) { return 2 }
        }
        if let base = parts.last, !base.isEmpty, title.contains(base) {
            return 1
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
