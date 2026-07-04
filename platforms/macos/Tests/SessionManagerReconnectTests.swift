import XCTest
@testable import Irrlicht
import Foundation

/// Regression coverage for #843: the macOS app never recovered when the
/// daemon it was connected to died and a new one started on the same port.
///
/// Two bugs combined to cause that. First, `connect()` reset `reconnectDelay`
/// back to 1.0 on every attempt — including failed ones — before the
/// exponential backoff could ever take effect, so a dead daemon got hammered
/// every ~1.1s forever instead of backing off. Second, `URLSession.shared`
/// could get stuck against a host:port that restarted under it, failing
/// forever even once a healthy daemon was listening again; only an app
/// relaunch (a fresh process, and so a fresh URLSession) recovered.
///
/// `connect()` itself does real networking and isn't exercised here (see
/// SessionManagerTests's issue #832 note on why tests never dial the real
/// daemon); these tests pin the two state transitions it delegates to.
@MainActor
final class SessionManagerReconnectTests: XCTestCase {
    private var sut: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        sut = SessionManager()
    }

    override func tearDown() async throws {
        sut = nil
        try await super.tearDown()
    }

    func testRecordConfirmedLocalConnect_resetsBackoffAndFailureState() {
        // Simulate having backed off after several failed attempts.
        sut.reconnectDelay = 16.0
        sut.consecutiveLocalConnectFailures = 2
        sut.localConnectionStalled = true
        sut.connectionState = .reconnecting

        sut.recordConfirmedLocalConnect()

        XCTAssertEqual(sut.reconnectDelay, 1.0,
                       "a confirmed connect must reset the backoff for the next disconnect")
        XCTAssertEqual(sut.consecutiveLocalConnectFailures, 0)
        XCTAssertFalse(sut.localConnectionStalled)
        XCTAssertEqual(sut.connectionState, .connected)
    }

    func testRecordFailedLocalConnectAttempt_doesNotRecycleBeforeThreshold() {
        let originalSession = sut.localURLSession

        for _ in 0..<(sut.localConnectFailuresBeforeSessionRecycle - 1) {
            XCTAssertFalse(sut.recordFailedLocalConnectAttempt())
        }

        XCTAssertFalse(sut.localConnectionStalled, "must not surface stalled before the threshold")
        XCTAssertTrue(sut.localURLSession === originalSession, "session must survive isolated blips")
    }

    func testRecordFailedLocalConnectAttempt_recyclesSessionAtThreshold() {
        let originalSession = sut.localURLSession

        for _ in 0..<(sut.localConnectFailuresBeforeSessionRecycle - 1) {
            sut.recordFailedLocalConnectAttempt()
        }
        let recycled = sut.recordFailedLocalConnectAttempt()

        XCTAssertTrue(recycled, "the Nth consecutive failure must trigger a recycle")
        XCTAssertTrue(sut.localConnectionStalled, "a recycle surfaces as stalled for the UI")
        XCTAssertFalse(sut.localURLSession === originalSession,
                       "a stuck URLSession must be discarded, not reused (#843)")
        XCTAssertEqual(sut.consecutiveLocalConnectFailures, 0, "the streak resets after recycling")
    }

    func testStartWebSocket_clearsStaleFailureState() {
        sut.consecutiveLocalConnectFailures = sut.localConnectFailuresBeforeSessionRecycle
        sut.localConnectionStalled = true

        sut.startWebSocket()

        XCTAssertEqual(sut.consecutiveLocalConnectFailures, 0)
        XCTAssertFalse(sut.localConnectionStalled)
        sut.stopWebSocket() // cancel the scheduled connect before it can dial out
    }

    func testStopWebSocket_clearsStalledFlag() {
        sut.localConnectionStalled = true

        sut.stopWebSocket()

        XCTAssertFalse(sut.localConnectionStalled)
    }
}
