import XCTest
@testable import Irrlicht
import Foundation

/// Regression coverage for #846: the relay reconnect loop dialed out on the
/// never-recycled `URLSession.shared`, so a standalone `irrlichtrelay`
/// restarting on the same host:port could wedge the connection the same way
/// a restarted local daemon did before #843 — see
/// `SessionManagerReconnectTests` for that story. This mirrors those tests
/// for the relay path's own dedicated `relayURLSession`.
@MainActor
final class SessionManagerRelayReconnectTests: XCTestCase {
    private var sut: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        sut = SessionManager()
    }

    override func tearDown() async throws {
        sut = nil
        try await super.tearDown()
    }

    func testRecordConfirmedRelayConnect_resetsBackoffAndFailureState() {
        sut.relayReconnectDelay = 16.0
        sut.consecutiveRelayConnectFailures = 2
        sut.relayConnectionStalled = true

        sut.recordConfirmedRelayConnect()

        XCTAssertEqual(sut.relayReconnectDelay, 1.0,
                       "a confirmed connect must reset the backoff for the next disconnect")
        XCTAssertEqual(sut.consecutiveRelayConnectFailures, 0)
        XCTAssertFalse(sut.relayConnectionStalled)
    }

    func testRecordFailedRelayConnectAttempt_doesNotRecycleBeforeThreshold() {
        let originalSession = sut.relayURLSession

        for _ in 0..<(sut.relayConnectFailuresBeforeSessionRecycle - 1) {
            XCTAssertFalse(sut.recordFailedRelayConnectAttempt())
        }

        XCTAssertFalse(sut.relayConnectionStalled, "must not surface stalled before the threshold")
        XCTAssertTrue(sut.relayURLSession === originalSession, "session must survive isolated blips")
    }

    func testRecordFailedRelayConnectAttempt_recyclesSessionAtThreshold() {
        let originalSession = sut.relayURLSession

        for _ in 0..<(sut.relayConnectFailuresBeforeSessionRecycle - 1) {
            sut.recordFailedRelayConnectAttempt()
        }
        let recycled = sut.recordFailedRelayConnectAttempt()

        XCTAssertTrue(recycled, "the Nth consecutive failure must trigger a recycle")
        XCTAssertTrue(sut.relayConnectionStalled, "a recycle surfaces as stalled for the UI")
        XCTAssertFalse(sut.relayURLSession === originalSession,
                       "a stuck relay URLSession must be discarded, not reused (#846)")
        XCTAssertEqual(sut.consecutiveRelayConnectFailures, 0, "the streak resets after recycling")
    }

    func testResetRelayConnectBackoff_clearsStaleFailureState() {
        sut.relayReconnectDelay = 8.0
        sut.consecutiveRelayConnectFailures = sut.relayConnectFailuresBeforeSessionRecycle
        sut.relayConnectionStalled = true

        sut.resetRelayConnectBackoff()

        XCTAssertEqual(sut.relayReconnectDelay, 1.0)
        XCTAssertEqual(sut.consecutiveRelayConnectFailures, 0)
        XCTAssertFalse(sut.relayConnectionStalled)
    }

    func testStopRelay_clearsStalledFlag() {
        sut.relayConnectionStalled = true

        sut.stopRelay()

        XCTAssertFalse(sut.relayConnectionStalled)
    }
}
