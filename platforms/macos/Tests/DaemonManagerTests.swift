import XCTest
@testable import Irrlicht
import Foundation

@MainActor
final class DaemonManagerTests: XCTestCase {
    var manager: DaemonManager!

    override func setUp() async throws {
        try await super.setUp()
        manager = DaemonManager()
    }

    override func tearDown() async throws {
        manager.stop()
        manager = nil
        try await super.tearDown()
    }

    // MARK: - Health Check Tests

    func testDaemonNotReachableWhenNothingListening() async {
        // Port 7837 should not be reachable in CI / fresh test env
        // (unless irrlichd happens to be running — skip gracefully).
        let reachable = await manager.isDaemonReachable()
        // We can't assert false because irrlichd might actually be running.
        // Instead just ensure the method completes without crashing.
        _ = reachable
    }

    func testInitialState() {
        XCTAssertFalse(manager.daemonRunning)
    }

    func testStopIsIdempotent() {
        manager.stop()
        manager.stop()
        XCTAssertFalse(manager.daemonRunning)
    }

    // MARK: - Daemon launch env (issue #722)

    func testBuildDaemonEnvSetsBindAddrAndPreservesBase() {
        let env = DaemonManager.buildDaemonEnv(
            base: ["PATH": "/usr/bin"],
            bindAddr: "127.0.0.1:7838"
        )
        XCTAssertEqual(env["IRRLICHT_BIND_ADDR"], "127.0.0.1:7838")
        XCTAssertEqual(env["PATH"], "/usr/bin", "base environment must be preserved")
    }

    func testBuildDaemonEnvNoLongerLayersRelayPublishVars() {
        // Relay publishing now travels over loopback (issue #722), not launch
        // env: buildDaemonEnv must not inject IRRLICHT_RELAY_* of its own. An
        // inherited value is left untouched (the app's own environment), but the
        // builder adds nothing.
        let env = DaemonManager.buildDaemonEnv(
            base: [:],
            bindAddr: "127.0.0.1:7837"
        )
        XCTAssertNil(env["IRRLICHT_RELAY_URL"])
        XCTAssertNil(env["IRRLICHT_RELAY_TOKEN"])
    }

    func testPublishSettingsDidChangeDoesNotRelaunchDaemon() {
        // The toggle now POSTs to the running daemon instead of relaunching it.
        // Under xctest there's no daemon binary and no app-owned process, so the
        // call must be a safe fire-and-forget no-op — never spawn or crash.
        XCTAssertNil(manager.currentProcessForTesting, "precondition: no app-owned daemon")
        manager.publishSettingsDidChange()
        manager.publishSettingsDidChange()
        XCTAssertFalse(manager.daemonRunning, "publishSettingsDidChange must not launch a daemon")
        XCTAssertNil(manager.currentProcessForTesting, "publishSettingsDidChange must not spawn a process")
    }

    // MARK: - Bundle Path Tests

    func testBundledDaemonURLNilInTestHost() {
        // When running under xctest the bundle doesn't contain irrlichd,
        // so start() should handle the missing binary gracefully.
        manager.start()

        // Give async task a moment to execute
        let expectation = XCTestExpectation(description: "start completes without crash")
        Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 0.5 s
            expectation.fulfill()
        }
        wait(for: [expectation], timeout: 2)

        // Should not crash — daemonRunning may be true if an external daemon is running,
        // or false if nothing is listening and the binary isn't in the bundle.
    }
}
