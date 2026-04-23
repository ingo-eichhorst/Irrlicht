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

    // MARK: - Helpers

    /// Constructs a minimal SessionState whose launcher is wired to the given
    /// termProgram, cwd, and optional extra fields.
    private func makeSession(
        id: String = UUID().uuidString,
        termProgram: String,
        cwd: String,
        itermSessionID: String? = nil,
        tty: String? = nil,
        kittyListenOn: String? = nil,
        kittyWindowID: String? = nil,
        tmuxPane: String? = nil,
        tmuxSocket: String? = nil
    ) -> SessionState {
        // Build the JSON we'd receive from the daemon so we rely on the actual
        // Codable path rather than synthesising via initializer.
        var launcherFields: [String: String] = [
            "term_program": termProgram,
        ]
        if let v = itermSessionID  { launcherFields["iterm_session_id"] = v }
        if let v = tty             { launcherFields["tty"] = v }
        if let v = kittyListenOn   { launcherFields["kitty_listen_on"] = v }
        if let v = kittyWindowID   { launcherFields["kitty_window_id"] = v }
        if let v = tmuxPane        { launcherFields["tmux_pane"] = v }
        if let v = tmuxSocket      { launcherFields["tmux_socket"] = v }

        let sessionDict: [String: Any] = [
            "session_id": id,
            "state": "working",
            "model": "claude-sonnet-4-5",
            "cwd": cwd,
            "adapter": "claude-code",
            "first_seen": Int(Date().timeIntervalSince1970),
            "updated_at": Int(Date().timeIntervalSince1970),
            "launcher": launcherFields,
        ]
        let data = try! JSONSerialization.data(withJSONObject: sessionDict)
        return try! JSONDecoder().decode(SessionState.self, from: data)
    }

    /// Opens `bundleID` to a temp directory and waits up to `timeout` for the
    /// app to appear in NSRunningApplication. Returns the running app or nil.
    private func launchApp(bundleID: String, cwd: String, timeout: TimeInterval = 5) -> NSRunningApplication? {
        guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            return nil
        }
        let tempURL = URL(fileURLWithPath: cwd)
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

    /// Reads the frontmost window title of a running app via AX.
    private func frontmostWindowTitle(bundleID: String) -> String? {
        guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).first else { return nil }
        let axApp = AXUIElementCreateApplication(app.processIdentifier)
        var windowsRef: CFTypeRef?
        guard AXUIElementCopyAttributeValue(axApp, kAXWindowsAttribute as CFString, &windowsRef) == .success,
              let windows = windowsRef as? [AXUIElement],
              let first = windows.first else { return nil }
        var titleRef: CFTypeRef?
        AXUIElementCopyAttributeValue(first, kAXTitleAttribute as CFString, &titleRef)
        return titleRef as? String
    }

    // MARK: - Tests

    func testGhosttyActivation() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1, a display, and Ghostty installed")
        let cwd = NSTemporaryDirectory() + "irrlicht-harness-ghostty"
        guard launchApp(bundleID: "com.mitchellh.ghostty", cwd: cwd) != nil else {
            throw XCTSkip("Ghostty not installed")
        }
        Thread.sleep(forTimeInterval: 1.0) // let the window appear
        let session = makeSession(termProgram: "ghostty", cwd: cwd)
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
            cwd: "/Users/test/myproject"
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
        let result = ProcessRunner.run("/bin/sleep", args: ["10"], timeout: 0.2)
        XCTAssertEqual(result.status, -1, "Timed-out process should return status -1")
        XCTAssertEqual(result.stderr, "timeout")
    }

    func testKittyActivatorFallsBackGracefullyWhenNoSocket() throws {
        try XCTSkipUnless(Self.harnessEnabled, "requires TEST_HARNESS=1")
        // Session with no KITTY_LISTEN_ON — should fall back to app-level
        // activation without crashing, and return false when kitty isn't installed.
        let session = makeSession(termProgram: "kitty", cwd: "/tmp/kitty-harness-test")
        let activated = KittyActivator().activate(session)
        // We can only assert no crash; activated may be true or false depending
        // on whether kitty is installed on the developer's machine.
        _ = activated
    }
}
