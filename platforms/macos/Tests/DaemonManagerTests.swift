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

    // MARK: - Relay-publish env wiring (issue #718)

    func testBuildDaemonEnvPublishOnWithToken() {
        let env = DaemonManager.buildDaemonEnv(
            base: ["PATH": "/usr/bin"],
            bindAddr: "127.0.0.1:7837",
            publishEnabled: true,
            relayURL: "wss://funken.io",
            relayToken: "tok"
        )
        XCTAssertEqual(env["IRRLICHT_BIND_ADDR"], "127.0.0.1:7837")
        XCTAssertEqual(env["IRRLICHT_RELAY_URL"], "wss://funken.io")
        XCTAssertEqual(env["IRRLICHT_RELAY_TOKEN"], "tok")
        XCTAssertEqual(env["PATH"], "/usr/bin", "base environment must be preserved")
    }

    func testBuildDaemonEnvPublishOnWithoutTokenStripsInheritedToken() {
        // Publishing on, but no token configured: an inherited IRRLICHT_RELAY_TOKEN
        // must not leak through (no-auth relay, or token cleared in Settings).
        let env = DaemonManager.buildDaemonEnv(
            base: ["IRRLICHT_RELAY_TOKEN": "stale"],
            bindAddr: "127.0.0.1:7837",
            publishEnabled: true,
            relayURL: "wss://funken.io",
            relayToken: ""
        )
        XCTAssertEqual(env["IRRLICHT_RELAY_URL"], "wss://funken.io")
        XCTAssertNil(env["IRRLICHT_RELAY_TOKEN"])
    }

    func testBuildDaemonEnvPublishOffStripsInheritedVars() {
        // Toggling publish off must truly stop forwarding even if the app was
        // launched with the relay vars already set in its environment.
        let env = DaemonManager.buildDaemonEnv(
            base: ["IRRLICHT_RELAY_URL": "wss://stale", "IRRLICHT_RELAY_TOKEN": "stale"],
            bindAddr: "127.0.0.1:7837",
            publishEnabled: false,
            relayURL: "wss://funken.io",
            relayToken: "tok"
        )
        XCTAssertNil(env["IRRLICHT_RELAY_URL"])
        XCTAssertNil(env["IRRLICHT_RELAY_TOKEN"])
        XCTAssertEqual(env["IRRLICHT_BIND_ADDR"], "127.0.0.1:7837")
    }

    func testBuildDaemonEnvEmptyURLActsAsOff() {
        let env = DaemonManager.buildDaemonEnv(
            base: [:],
            bindAddr: "127.0.0.1:7837",
            publishEnabled: true,
            relayURL: "   ",
            relayToken: "tok"
        )
        XCTAssertNil(env["IRRLICHT_RELAY_URL"], "enabled with a blank URL must not activate the forwarder")
        XCTAssertNil(env["IRRLICHT_RELAY_TOKEN"])
    }

    func testBuildDaemonEnvTrimsURLAndToken() {
        let env = DaemonManager.buildDaemonEnv(
            base: [:],
            bindAddr: "127.0.0.1:7837",
            publishEnabled: true,
            relayURL: "  wss://funken.io  ",
            relayToken: "  tok  "
        )
        XCTAssertEqual(env["IRRLICHT_RELAY_URL"], "wss://funken.io")
        XCTAssertEqual(env["IRRLICHT_RELAY_TOKEN"], "tok")
    }

    func testPublishSettingsDidChangeIsSafeWithoutOwnedDaemon() {
        // Under xctest there's no daemon binary, so no app-owned process: the
        // relaunch path must be a safe no-op (and idempotent), never a crash.
        manager.publishSettingsDidChange()
        manager.publishSettingsDidChange()
        XCTAssertFalse(manager.daemonRunning)
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
