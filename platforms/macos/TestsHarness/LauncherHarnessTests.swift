import AppKit
import ApplicationServices
import XCTest

@testable import Irrlicht

/// Integration tests for SessionLauncher activators. Each test launches a real
/// macOS app, waits for it to be ready, fires `SessionLauncher.jump`, and
/// asserts that the target app is frontmost via AX readback.
///
/// All tests are gated on `TEST_HARNESS=1` — they are skipped automatically
/// in CI (no display server or installed apps). Run locally with:
///
///     TEST_HARNESS=1 swift test --filter LauncherTestHarness
///
@MainActor
final class LauncherHarnessTests: XCTestCase {

    private static let harnessEnabled = ProcessInfo.processInfo.environment["TEST_HARNESS"] == "1"

    private typealias CanonicalPath = GhosttyActivator.CanonicalPath
    private typealias SurfaceID = GhosttyActivator.SurfaceID

    // MARK: - Helpers

    /// Bundles makeSession's optional launcher-identity fields, beyond the
    /// core termProgram/cwd pair every session needs.
    private struct LauncherOverrides {
        var itermSessionID: String? = nil
        var tty: String? = nil
        var kittyListenOn: String? = nil
        var kittyWindowID: String? = nil
        var kittyPID: Int? = nil
        var tmuxPane: String? = nil
        var tmuxSocket: String? = nil
    }

    /// Constructs a minimal SessionState whose launcher is wired to the given
    /// termProgram, cwd, and optional extra fields.
    private func makeSession(
        termProgram: String,
        cwd: CanonicalPath,
        launcher overrides: LauncherOverrides = LauncherOverrides()
    ) throws -> SessionState {
        // Build the JSON we'd receive from the daemon so we rely on the actual
        // Codable path rather than synthesising via initializer.
        var launcherFields: [String: Any] = [
            "term_program": termProgram,
        ]
        if let v = overrides.itermSessionID  { launcherFields["iterm_session_id"] = v }
        if let v = overrides.tty             { launcherFields["tty"] = v }
        if let v = overrides.kittyListenOn   { launcherFields["kitty_listen_on"] = v }
        if let v = overrides.kittyWindowID   { launcherFields["kitty_window_id"] = v }
        if let v = overrides.kittyPID        { launcherFields["kitty_pid"] = v }
        if let v = overrides.tmuxPane        { launcherFields["tmux_pane"] = v }
        if let v = overrides.tmuxSocket      { launcherFields["tmux_socket"] = v }

        let sessionDict: [String: Any] = [
            "session_id": UUID().uuidString,
            "state": "working",
            "model": "claude-sonnet-4-5",
            "cwd": cwd.value,
            "adapter": "claude-code",
            "first_seen": Int(Date().timeIntervalSince1970),
            "updated_at": Int(Date().timeIntervalSince1970),
            "launcher": launcherFields,
        ]
        let data = try JSONSerialization.data(withJSONObject: sessionDict)
        return try JSONDecoder().decode(SessionState.self, from: data)
    }

    /// Opens `bundleID` to a temp directory and waits up to `timeout` for the
    /// app to appear in NSRunningApplication. Returns the running app or nil.
    private func launchApp(bundleID: String, cwd: CanonicalPath, timeout: TimeInterval = 5) -> NSRunningApplication? {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            return nil
        }
        let tempURL = URL(fileURLWithPath: cwd.value)
        try? FileManager.default.createDirectory(at: tempURL, withIntermediateDirectories: true)
        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = false
        var launched: NSRunningApplication?
        let sem = DispatchSemaphore(value: 0)
        NSWorkspace.shared.openApplication(at: url, configuration: cfg) { app, _ in
            launched = app
            sem.signal()
        }
        sem.wait()
        // Wait for the app to be fully running.
        let deadline = Date(timeIntervalSinceNow: timeout)
        while launched?.isFinishedLaunching == false && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.1)
        }
        return launched
    }

    // MARK: - Tests

    func testGhosttyActivation() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1, a display, and Ghostty installed")
        let cwd = try XCTUnwrap(CanonicalPath(NSTemporaryDirectory() + "irrlicht-harness-ghostty"))
        guard launchApp(bundleID: Self.ghosttyBundleID, cwd: cwd) != nil else {
            throw XCTSkip("Ghostty not installed")
        }
        Thread.sleep(forTimeInterval: 1.0) // let the window appear
        let session = try makeSession(termProgram: "ghostty", cwd: cwd)
        SessionLauncher.jump(session)
        Thread.sleep(forTimeInterval: 0.5)
        let frontmost = NSWorkspace.shared.frontmostApplication?.bundleIdentifier
        XCTAssertEqual(frontmost, "com.mitchellh.ghostty", "Ghostty should be frontmost after jump")
    }

    func testAXTitleMatchActivatorDoesNotCrashWithNoWindows() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1")
        // Call raiseMatchingWindow for a bundle that has no running instance.
        // Must not crash or throw — just silently return.
        AXTitleMatchActivator.raiseMatchingWindow(
            bundleID: "com.nonexistent.app.harness",
            cwd: "/Users/test/myproject"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        )
    }

    func testJetBrainsActivatorRunningBundleIDReturnsNilOrKnown() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1")
        if let bid = JetBrainsActivator.runningBundleID() {
            // If a JetBrains IDE is open, the bundle ID must be one we recognise.
            let knownBundleIDs = [
                "com.jetbrains.goland", "com.jetbrains.intellij", "com.jetbrains.intellij.ce",
                "com.jetbrains.pycharm", "com.jetbrains.pycharm.ce", "com.jetbrains.WebStorm",
                "com.jetbrains.rider", "com.jetbrains.CLion", "com.jetbrains.rustrover",
            ]
            XCTAssertTrue(knownBundleIDs.contains(bid), "Unexpected JetBrains bundle ID: \(bid)")
        }
        // nil is also valid (no IDE open).
    }

    func testProcessRunnerTimesOut() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1")
        let result = ProcessRunner.run("/bin/sleep", args: ["10"], timeout: 0.2)  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        XCTAssertEqual(result.status, -1, "Timed-out process should return status -1")
        XCTAssertEqual(result.stderr, "timeout")
    }

    func testKittyActivatorFallsBackGracefullyWhenNoSocket() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1")
        // Session with no KITTY_LISTEN_ON — should fall back to app-level
        // activation without crashing, and return false when kitty isn't installed.
        let cwd = try XCTUnwrap(CanonicalPath(NSTemporaryDirectory() + "kitty-harness-test"))
        let session = try makeSession(termProgram: "kitty", cwd: cwd)
        let activated = KittyActivator().activate(session)
        // We can only assert no crash; activated may be true or false depending
        // on whether kitty is installed on the developer's machine.
        _ = activated
    }

    /// Round-trips the new `kitty_pid` JSON field through the SessionState
    /// decoder to confirm the field is exposed to the activator. The activator
    /// itself reads `session.launcher?.kittyPID` to pick between the
    /// PID-targeted path and the bundle-fallback path; if this field doesn't
    /// decode, every click silently degrades to the bundle fallback (which is
    /// what issue #326 was actually exhibiting before the fix).
    func testKittyLauncherDecodesKittyPID() throws {
        let session = try makeSession(
            termProgram: "kitty",
            cwd: try XCTUnwrap(CanonicalPath(NSTemporaryDirectory() + "kitty-decode-test")),
            launcher: LauncherOverrides(
                kittyListenOn: "unix:/tmp/kitty-12345",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
                kittyWindowID: "2",
                kittyPID: 12345
            )
        )
        XCTAssertEqual(session.launcher?.kittyPID, 12345)
        XCTAssertEqual(session.launcher?.kittyListenOn, "unix:/tmp/kitty-12345")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        XCTAssertEqual(session.launcher?.kittyWindowID, "2")
    }

    // MARK: - Ghostty tab selection

    /// NOT yet seen red: `titleMatchScore` returns 0 for these titles by calculation, never by a recorded run.
    func testGhosttyJumpSelectsTheTabMatchingSessionCwd() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1, a display, and Ghostty installed")
        guard NSWorkspace.shared.urlForApplication(withBundleIdentifier: Self.ghosttyBundleID) != nil else {
            throw XCTSkip("Ghostty not installed")
        }
        guard Self.ghosttyIsRunningWithAWindow() else {
            throw XCTSkip("Ghostty has no open window; this test adds tabs to an existing one rather than cold-launching over the user's own session")
        }

        let target = try Self.makeSymlinkResolvedTempDir("ghostty-target")
        let decoy = try Self.makeSymlinkResolvedTempDir("ghostty-decoy")
        defer { Self.removeDirectories(target, decoy) }

        guard let targetID = Self.openGhosttyTabTitledLikeAnAgent(cwd: target, title: "Fix the flaky test"),
              let decoyID = Self.openGhosttyTabTitledLikeAnAgent(cwd: decoy, title: "Add pagination to the list")
        else {
            throw XCTSkip("could not open Ghostty tabs via AppleScript")
        }
        defer { Self.closeGhosttyTerminals(targetID, decoyID) }

        Self.parkSelectionOn(decoyID)
        XCTAssertEqual(
            Self.selectedGhosttyCwd(), decoy,
            "arrange: the decoy must hold the selection, else a jump that moves nothing would pass"
        )

        SessionLauncher.jump(try makeSession(termProgram: "ghostty", cwd: target))

        XCTAssertTrue(
            Self.waitUntil(timeout: 5) { Self.selectedGhosttyCwd() == target },
            "jump should select the tab whose working directory is \(target.value); selected is \(Self.selectedGhosttyCwd()?.value ?? "nil")"
        )
    }

    func testGhosttyJumpDeclinesWhenTwoTabsShareTheCwd() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1, a display, and Ghostty installed")
        guard NSWorkspace.shared.urlForApplication(withBundleIdentifier: Self.ghosttyBundleID) != nil else {
            throw XCTSkip("Ghostty not installed")
        }
        guard Self.ghosttyIsRunningWithAWindow() else {
            throw XCTSkip("Ghostty has no open window")
        }

        let shared = try Self.makeSymlinkResolvedTempDir("ghostty-ambiguous")
        let parked = try Self.makeSymlinkResolvedTempDir("ghostty-parked")
        defer { Self.removeDirectories(shared, parked) }

        guard let firstID = Self.openGhosttyTabTitledLikeAnAgent(cwd: shared, title: "Agent one"),
              let secondID = Self.openGhosttyTabTitledLikeAnAgent(cwd: shared, title: "Agent two"),
              let parkedID = Self.openGhosttyTabTitledLikeAnAgent(cwd: parked, title: "Somewhere else")
        else {
            throw XCTSkip("could not open Ghostty tabs via AppleScript")
        }
        defer { Self.closeGhosttyTerminals(firstID, secondID, parkedID) }

        Self.parkSelectionOn(parkedID)
        XCTAssertEqual(Self.selectedGhosttyCwd(), parked, "arrange: parked tab holds the selection")

        SessionLauncher.jump(try makeSession(termProgram: "ghostty", cwd: shared))
        Self.settleAsyncActivation()

        XCTAssertEqual(
            Self.selectedGhosttyCwd(), parked,
            "two tabs share \(shared.value) and nothing Ghostty exposes tells them apart, so the selection must not move"
        )
    }

    // MARK: - Ghostty harness helpers

    private static let ghosttyBundleID = "com.mitchellh.ghostty"

    private static func ghosttyScript(_ source: String) -> String? {
        AppleScriptRunner.run(source, tag: "harness-ghostty")
    }

    private static func ghosttyIsRunningWithAWindow() -> Bool {
        guard !NSRunningApplication.runningApplications(withBundleIdentifier: ghosttyBundleID).isEmpty else {
            return false
        }
        let count = ghosttyScript(#"tell application "Ghostty" to return (count of windows) as text"#)
        return (count.flatMap(Int.init) ?? 0) > 0
    }

    private static func makeSymlinkResolvedTempDir(_ prefix: String) throws -> CanonicalPath {
        let url = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("irrlicht-harness-\(prefix)-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return try XCTUnwrap(CanonicalPath(url.path))
    }

    private static func removeDirectories(_ paths: CanonicalPath...) {
        for path in paths { try? FileManager.default.removeItem(atPath: path.value) }
    }

    /// Overwrites Ghostty's default path-shaped title the way a real agent does.
    private static func openGhosttyTabTitledLikeAnAgent(cwd: CanonicalPath, title: String) -> SurfaceID? {
        let safeCwd = AppleScriptRunner.escape(cwd.value)
        let raw = ghosttyScript("""
        tell application "Ghostty"
            set t to new tab in front window with configuration {initial working directory:"\(safeCwd)"}
            return id of (focused terminal of t)
        end tell
        """)
        guard let raw, !raw.isEmpty else { return nil }
        let id = SurfaceID(raw)
        _ = waitUntil(timeout: 5) { cwdOfGhosttyTerminal(id: id) != nil }
        _ = ghosttyScript("""
        tell application "Ghostty"
            try
                perform action "set_tab_title:\(AppleScriptRunner.escape(title))" on (first terminal whose id is "\(AppleScriptRunner.escape(id.value))")
            end try
        end tell
        """)
        return id
    }

    private static func closeGhosttyTerminals(_ ids: SurfaceID...) {
        for id in ids {
            _ = ghosttyScript("""
            tell application "Ghostty"
                try
                    close (first terminal whose id is "\(AppleScriptRunner.escape(id.value))")
                end try
            end tell
            """)
        }
    }

    private static func parkSelectionOn(_ id: SurfaceID) {
        _ = ghosttyScript("""
        tell application "Ghostty"
            focus (first terminal whose id is "\(AppleScriptRunner.escape(id.value))")
        end tell
        """)
        Thread.sleep(forTimeInterval: 0.4)
    }

    /// Waiting out the whole budget, because polling for "nothing happened" returns instantly.
    private static func settleAsyncActivation() {
        Thread.sleep(forTimeInterval: 5)
    }

    private static func cwdOfGhosttyTerminal(id: SurfaceID) -> CanonicalPath? {
        let cwd = ghosttyScript("""
        tell application "Ghostty"
            try
                return working directory of (first terminal whose id is "\(AppleScriptRunner.escape(id.value))")
            on error
                return ""
            end try
        end tell
        """)
        return cwd.flatMap(CanonicalPath.init)
    }

    /// Canonical, which is what retired this harness's own samePath helper.
    private static func selectedGhosttyCwd() -> CanonicalPath? {
        let cwd = ghosttyScript("""
        tell application "Ghostty"
            try
                return working directory of (focused terminal of (selected tab of front window))
            on error
                return ""
            end try
        end tell
        """)
        return cwd.flatMap(CanonicalPath.init)
    }

    private static func waitUntil(timeout: TimeInterval, _ condition: () -> Bool) -> Bool {
        let deadline = Date(timeIntervalSinceNow: timeout)
        while Date() < deadline {
            if condition() { return true }
            Thread.sleep(forTimeInterval: 0.2)
        }
        return condition()
    }
}
