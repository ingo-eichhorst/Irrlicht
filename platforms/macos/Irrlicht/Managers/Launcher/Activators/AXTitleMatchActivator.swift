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

    static func raiseMatchingWindow(bundleID: String, cwd: String) {
        guard AccessibilityPermission.ensureTrusted() else {
            logger.info("AX permission not granted — staying on app-level activation")
            return
        }
        let runningApps = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
        guard !runningApps.isEmpty else { return }

        // kAXWindowsAttribute omits windows that are fullscreen on
        // another Space for Electron hosts (VS Code, Cursor, Windsurf), so
        // we enumerate via the app's Window menu instead — it lists every
        // open window across Spaces, and AX-pressing an item is what the
        // user would do manually, so macOS handles the Space switch and
        // raise atomically.
        var candidates: [(menuItem: AXUIElement, title: String)] = []
        for app in runningApps {
            let axApp = AXUIElementCreateApplication(app.processIdentifier)
            candidates.append(contentsOf: windowMenuItems(axApp: axApp))
        }
        let titles = candidates.map { $0.title }
        guard let idx = bestMatchIndex(titles: titles, cwd: cwd) else {
            logger.info("no window menu item matched cwd \(cwd, privacy: .public); candidates=\(titles, privacy: .public)")
            return
        }
        let target = candidates[idx]
        logger.info("AX dispatch: cwd=\(cwd, privacy: .public) picked=[\(idx)] \(titles[idx], privacy: .public) of candidates=\(titles, privacy: .public)")
        AXUIElementPerformAction(target.menuItem, kAXPressAction as CFString)
    }

    /// Returns (menuItem, title) pairs for every entry in the app's Window
    /// menu. The Window menu is identified by its localized title; if
    /// the app's locale isn't in our list we fail gracefully (returns
    /// `[]`) rather than guessing at a positional fallback that might
    /// land on a non-Window menu and press a destructive item.
    ///
    /// Non-window entries in the menu (Minimize, Zoom, …) are not
    /// filtered explicitly — `bestMatchIndex` scores them 0 against any
    /// real cwd, so they never win regardless of locale.
    private static let windowMenuTitles: Set<String> = [
        "Window",       // en
        "Fenster",      // de
        "Fenêtre",      // fr
        "Ventana",      // es
        "Finestra",     // it
        "Janela",       // pt (BR & PT)
        "Venster",      // nl
        "Fönster",      // sv
        "Vindue",       // da
        "Vindu",        // no/nb
        "Ikkuna",       // fi
        "Okno",         // pl/cs
        "Окно",         // ru
        "Pencere",      // tr
        "ウィンドウ",      // ja
        "窗口",          // zh-Hans
        "視窗",          // zh-Hant
        "창",           // ko
    ]

    private static func windowMenuItems(axApp: AXUIElement) -> [(menuItem: AXUIElement, title: String)] {
        var menuBarRef: CFTypeRef?
        // CFGetTypeID crashes on NULL and Swift CF bridging doesn't allow
        // `as? AXUIElement`, so we unwrap then runtime-check the type.
        guard AXUIElementCopyAttributeValue(axApp, kAXMenuBarAttribute as CFString, &menuBarRef) == .success,
              let menuBarRef,
              CFGetTypeID(menuBarRef) == AXUIElementGetTypeID()
        else { return [] }
        let menuBar = menuBarRef as! AXUIElement

        guard let windowMenu = axChildren(menuBar).first(where: { menu in
            axTitle(menu).map(windowMenuTitles.contains) ?? false
        }) else { return [] }

        // A top-level menu has a single AXMenu popup child; its children
        // are the menu items.
        guard let popup = axChildren(windowMenu).first else { return [] }

        return axChildren(popup).compactMap { item in
            guard let title = axTitle(item), !title.isEmpty else { return nil }
            return (item, title)
        }
    }

    private static func axChildren(_ element: AXUIElement) -> [AXUIElement] {
        var ref: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, kAXChildrenAttribute as CFString, &ref) == .success,
              let children = ref as? [AXUIElement]
        else { return [] }
        return children
    }

    private static func axTitle(_ element: AXUIElement) -> String? {
        var ref: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, kAXTitleAttribute as CFString, &ref) == .success
        else { return nil }
        return ref as? String
    }

    // MARK: - Title match (pure, testable)

    /// Generic root-level path segments that must never serve as a match
    /// signal — they appear in virtually every home-directory path string.
    private static let genericTopSegments: Set<String> = [
        "Users", "home", "tmp", "var", "private", "opt", "mnt", "root"
    ]
    private static let homeBasename: String = {
        (ProcessInfo.processInfo.environment["HOME"] ?? "")
            .split(separator: "/").last.map(String.init) ?? ""
    }()

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
    ///
    /// Primary sort: `titleMatchScore` (higher is better). Tie-break: count of
    /// meaningful CWD path segments that appear in the title — handles the case
    /// where two windows share the same leaf folder name (e.g. two repos both
    /// called `irrlicht`) and one of them also contains the grandparent segment
    /// (e.g. `a/irrlicht` vs `b/irrlicht`). AX returns windows in z-order, so
    /// first occurrence wins within the same (score, prefixLen) bucket.
    static func bestMatchIndex(titles: [String], cwd: String) -> Int? {
        var best: (idx: Int, score: Int, prefixLen: Int)?
        for (i, t) in titles.enumerated() {
            let s = titleMatchScore(title: t, cwd: cwd)
            if s == 0 { continue }
            let p = cwdSegmentMatchCount(title: t, cwd: cwd)
            if best == nil || s > best!.score || (s == best!.score && p > best!.prefixLen) {
                best = (i, s, p)
            }
        }
        return best?.idx
    }

    /// Counts how many non-generic CWD path segments appear anywhere in the
    /// title. Used as a tie-breaker when two titles share the same primary
    /// score — a title mentioning both `irrlicht` and `a` beats one that
    /// mentions only `irrlicht`.
    static func cwdSegmentMatchCount(title: String, cwd: String) -> Int {
        if title.isEmpty || cwd.isEmpty { return 0 }
        let trimmed = cwd.hasSuffix("/") ? String(cwd.dropLast()) : cwd
        let segments = trimmed
            .split(separator: "/", omittingEmptySubsequences: true)
            .map(String.init)
            .filter { p in
                !p.isEmpty &&
                !genericTopSegments.contains(p) &&
                (homeBasename.isEmpty || p != homeBasename)
            }
        return segments.filter { title.contains($0) }.count
    }
}
