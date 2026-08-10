import AppKit
import Foundation
import OSLog

/// Ghostty activator: selects the terminal surface whose working directory is
/// the session's cwd, via Ghostty's scripting dictionary.
///
/// Why not `AXTitleMatchActivator`, which Ghostty used before. The AX
/// machinery is fine — measured, pressing a Ghostty Window-menu entry *does*
/// select that tab, including inside a native NSWindow tab group. What fails
/// is the key it matches on. `titleMatchScore` needs a path segment of the cwd
/// to appear in the tab title, and an agent's tab title is whatever the agent
/// set it to: for a cwd of `…/IdeaProjects/Irrlicht`, the title
/// `✳ Debug Irrlicht navigation` scores 4 while `✳ Fix the flaky test` scores
/// 0 — same session, same directory, and the second one silently raises the
/// window without touching the tab. Ghostty also abbreviates its own titles
/// (`~/…`, `…/…`), so the exact-cwd tier that would otherwise dominate can
/// never fire. The failure is a coin flip on the agent's phrasing, which is
/// why it reads as "sometimes it works".
///
/// A cwd is the same session's property but is not phrasing-dependent, so it
/// turns that coin flip into a decidable question.
///
/// **This is a cwd match, not an identity match, and the difference is the
/// design.** iTerm2, Terminal.app and kitty each land on the exact tab because
/// the host publishes a per-tab identity — `$ITERM_SESSION_ID`, a scriptable
/// `tty`, `$KITTY_WINDOW_ID`. Ghostty publishes neither form: it exports no
/// per-surface variable to child processes, its scripting `terminal` class
/// carries only `id`, `name` and `working directory`, and it has no remote
/// control CLI. So the only join available from outside is the working
/// directory, and two surfaces can share one.
///
/// The activator therefore acts **only on a unique match**, and the two ways
/// of not having one are handled differently on purpose:
///
/// - **Zero matches** — the good key found nothing (shell integration off, so
///   Ghostty reports a stale directory; or the agent's cwd is not the shell's).
/// - **Several matches** — two surfaces really are in that directory, and
///   nothing Ghostty exposes can tell them apart. A wrong pick lands the user
///   in a *different agent's* terminal.
///
/// Both raise the window and leave the tab selection alone. Neither falls back
/// to the old title match — see `raiseWithoutChangingSelection()` for the
/// measurement that decided it.
///
/// Ordering follows `TerminalAppActivator`: AppleScript owns activation
/// end-to-end and `activate` runs last, after the surface is focused.
/// Activating first lets Ghostty race to the foreground while we are still
/// selecting, and the previously frontmost window shows through until the next
/// click.
struct GhosttyActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "GhosttyActivator")

    let termProgram = "ghostty"
    let bundleID = "com.mitchellh.ghostty"

    /// One Ghostty terminal surface as reported by the scripting dictionary.
    /// A "surface" is a single pane: a tab holds one or more of them, so this
    /// is a finer unit than a tab and `focus` on it selects the owning tab.
    struct Surface: Equatable {
        let id: String
        let workingDirectory: String
    }

    func activate(_ session: SessionState) -> Bool {
        let cwd = session.cwd
        guard !cwd.isEmpty else {
            Self.logger.info("ghostty: no cwd for session \(session.id, privacy: .public)")
            return false
        }
        guard NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) != nil else {
            Self.logger.info("ghostty: no installed app for bundle id \(self.bundleID, privacy: .public)")
            return false
        }
        let bid = bundleID
        DispatchQueue.global(qos: .userInitiated).async {
            Self.selectSurface(cwd: cwd, bundleID: bid)
        }
        return true
    }

    // MARK: - Selection

    private static func selectSurface(cwd: String, bundleID: String) {
        guard let raw = AppleScriptRunner.run(enumerateSurfacesSource, tag: "Ghostty") else {
            // Either Ghostty predates the scripting dictionary, or Automation
            // consent for Ghostty was denied. Both are the user's environment
            // rather than an error we can fix, so degrade instead of giving up
            // — no version probe needed, the send either works or it doesn't.
            logger.info("ghostty: surfaces unreadable (no scripting dictionary, or Automation denied) — raising the window without changing the selection")
            raiseWithoutChangingSelection()
            return
        }
        let surfaces = parseSurfaces(raw)
        let matches = matchCount(surfaces: surfaces, cwd: cwd)
        if matches == 1, let hit = uniqueMatch(surfaces: surfaces, cwd: cwd) {
            focus(surfaceID: hit.id, cwd: cwd)
            return
        }
        if matches == 0 {
            logger.info("ghostty: no surface reports cwd \(cwd, privacy: .public) (of \(surfaces.count)) — raising the window without changing the selection. If Ghostty's shell-integration 'path' feature is off it reports a stale directory and nothing here can match.")
            raiseWithoutChangingSelection()
            return
        }
        logger.info("ghostty: \(matches) surfaces share cwd \(cwd, privacy: .public) — nothing Ghostty exposes can tell them apart, so the tab selection is left alone")
        raiseWithoutChangingSelection()
    }

    private static func focus(surfaceID: String, cwd: String) {
        let safe = AppleScriptRunner.escape(surfaceID)
        let source = """
        tell application "Ghostty"
            try
                focus (first terminal whose id is "\(safe)")
            on error
                return "0"
            end try
            activate
            return "1"
        end tell
        """
        if AppleScriptRunner.run(source, tag: "Ghostty") != "1" {
            logger.info("ghostty: focus failed for surface \(surfaceID, privacy: .public) (cwd \(cwd, privacy: .public))")
        }
    }

    /// Bring Ghostty forward and stop.
    ///
    /// Both non-unique cases end here, and neither is routed through
    /// `AXTitleMatchActivator`, which is a deliberate reversal of this file's
    /// first draft. Pressing a Window-menu item via `kAXPressAction` makes
    /// AppKit *open* that menu and run an `NSMenuTrackingSession` on the
    /// target app's main thread. Usually the item is then invoked and the
    /// session ends. Observed at least once against Ghostty 1.3.1, it does
    /// not: the main thread stays parked in
    /// `-[NSMenuTrackingSession startRunningMenuEventLoop:]`, and from there
    /// Ghostty answers no Apple Events, cannot be activated, and does not
    /// repaint — captured in a `sample(1)` trace. A click-to-jump that can
    /// freeze the terminal it is jumping to is a worse outcome than a jump
    /// that does not move the tab, so the Ghostty path does not press menus
    /// at all.
    ///
    /// What is given up is small and only in the zero-match case: a fuzzy
    /// title guess that, per this type's header, returns 0 for any agent
    /// title that happens not to repeat the folder name. The AX path stays in
    /// place for the hosts that have no better key.
    ///
    /// Activation on its own is safe — measured, `NSWorkspace.openApplication`,
    /// AppleScript `activate` and `NSRunningApplication.activate` all leave the
    /// selected tab exactly where it was.
    private static func raiseWithoutChangingSelection() {
        _ = AppleScriptRunner.run(#"tell application "Ghostty" to activate"#, tag: "Ghostty")
    }

    // MARK: - Matching (pure, testable)

    /// The surface to focus for `cwd`, or nil when the answer is not unique.
    ///
    /// Nil covers both "no surface is in that directory" and "several are",
    /// deliberately: neither is answerable from what Ghostty exposes, and the
    /// caller's response to both is the same — raise the window, leave the
    /// selection alone.
    static func uniqueMatch(surfaces: [Surface], cwd: String) -> Surface? {
        let matches = matching(surfaces: surfaces, cwd: cwd)
        return matches.count == 1 ? matches[0] : nil
    }

    static func matchCount(surfaces: [Surface], cwd: String) -> Int {
        matching(surfaces: surfaces, cwd: cwd).count
    }

    private static func matching(surfaces: [Surface], cwd: String) -> [Surface] {
        guard let want = normalizedPath(cwd) else { return [] }
        return surfaces.filter { normalizedPath($0.workingDirectory) == want }
    }

    /// Canonical form of a filesystem path, or nil when there isn't one.
    ///
    /// Resolving symlinks is not cosmetic here: macOS hands out temp
    /// directories as `/var/folders/…` while the same directory reports as
    /// `/private/var/folders/…`, and `/tmp` vs `/private/tmp` is the same
    /// story. A raw string compare misses those, and the miss is silent — it
    /// looks exactly like "no tab is in that directory".
    ///
    /// `resolvingSymlinksInPath()` alone does **not** settle it, which is the
    /// non-obvious part. It is existence-dependent in both directions: it
    /// leaves a path untouched when the file does not exist, and it strips a
    /// leading `/private` only "provided the result is the name of an existing
    /// file". So `/tmp/x` and `/private/tmp/x` normalise to *different*
    /// strings when `x` is gone — a session whose directory was deleted or
    /// renamed, which is exactly when someone is clicking the row to go look
    /// at it. The lexical strip below runs last so both spellings land on the
    /// same answer whether or not the directory is still there.
    static func normalizedPath(_ path: String) -> String? {
        guard !path.isEmpty else { return nil }
        let resolved = URL(fileURLWithPath: path).standardizedFileURL.resolvingSymlinksInPath().path
        return withoutPrivatePrefix(resolved)
    }

    /// The three roots macOS reaches through `/private`. Both spellings are in
    /// everyday circulation — `NSTemporaryDirectory()` hands back `/var/…`
    /// while a shell in the same directory reports `/private/var/…` — so one
    /// of them has to be chosen as canonical, and it may as well be the short
    /// one users type.
    private static let privateRoots = ["/tmp", "/var", "/etc"]

    private static func withoutPrivatePrefix(_ path: String) -> String {
        guard path.hasPrefix("/private/") else { return path }
        let tail = String(path.dropFirst("/private".count))
        for root in privateRoots where tail == root || tail.hasPrefix(root + "/") {
            return tail
        }
        return path
    }

    // MARK: - Scripting bridge

    /// Records are separated by ASCII RS (30) and the two fields within a
    /// record by ASCII US (31). Neither can occur in a path that any shell
    /// would report, which a tab or newline delimiter could not promise.
    private static let recordSeparator: Character = "\u{1E}"
    private static let unitSeparator: Character = "\u{1F}"

    private static let enumerateSurfacesSource = """
    tell application "Ghostty"
        set us to character id 31
        set rs to character id 30
        set out to ""
        repeat with s in terminals
            set out to out & (id of s) & us & (working directory of s) & rs
        end repeat
        return out
    end tell
    """

    static func parseSurfaces(_ raw: String) -> [Surface] {
        raw.split(separator: recordSeparator, omittingEmptySubsequences: true).compactMap { record in
            let parts = record.split(separator: unitSeparator, maxSplits: 1, omittingEmptySubsequences: false)
            guard parts.count == 2 else { return nil }
            let id = String(parts[0])
            guard !id.isEmpty else { return nil }
            return Surface(id: id, workingDirectory: String(parts[1]))
        }
    }
}
