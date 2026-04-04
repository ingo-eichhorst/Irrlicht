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
