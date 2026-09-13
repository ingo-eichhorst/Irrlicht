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

    func testRecordConfirmedRelayConnectResetsBackoffAndFailureState() {
        sut.relayReconnectDelay = 16.0
        sut.consecutiveRelayConnectFailures = 2
        sut.relayConnectionStalled = true

        sut.recordConfirmedRelayConnect()

        XCTAssertEqual(sut.relayReconnectDelay, 1.0,
                       "a confirmed connect must reset the backoff for the next disconnect")
        XCTAssertEqual(sut.consecutiveRelayConnectFailures, 0)
        XCTAssertFalse(sut.relayConnectionStalled)
    }

    /// #1953, red-first. `relayConnect()` used to set `.connected` eagerly, one
    /// line after `resume()` and before a single frame had arrived, so a relay
    /// socket left half-open by a standby/wake cycle stayed `.connected`
    /// forever: `aggregateConnectionState` stayed `.connected`, and
    /// `DaemonHealth.faults` returned `[]` at its `guard aggregate !=
    /// .connected`. Green dot, no banner, no data.
    ///
    /// `.connected` now means the same thing on both links — a frame actually
    /// arrived — which is `recordConfirmedRelayConnect()`'s job, mirroring
    /// `recordConfirmedLocalConnect()`.
    func testRecordConfirmedRelayConnectMarksTheLinkConnected() {
        sut.relayConnectionState = .connecting

        sut.recordConfirmedRelayConnect()

        XCTAssertEqual(sut.relayConnectionState, .connected,
                       "an arrived relay frame is what makes the link connected (#1953)")
    }

    /// Both signals that can confirm a relay cycle land on the same call, so a
    /// second confirmation must not re-arm the counters. Mirrors
    /// `SessionManagerReconnectTests`' idempotence test for the local path.
    func testRecordConfirmedRelayConnectIsIdempotent() {
        sut.recordConfirmedRelayConnect()
        sut.relayReconnectDelay = 16.0

        sut.recordConfirmedRelayConnect()

        XCTAssertEqual(sut.relayConnectionState, .connected)
        XCTAssertEqual(sut.relayReconnectDelay, 16.0,
                       "a repeat confirmation on an already-connected link is a no-op")
    }

    /// #1953. A liveness reap and a wake both end a cycle by cancelling a
    /// socket that was working — so the cycle-close bookkeeping has to be able
    /// to tell "the peer went away" from "we hung up on purpose". It does that
    /// by reading `relayConnectionState`, exactly as the local path does, and
    /// not a flag set inside the receive loop.
    ///
    /// Mutation, applied to the source and run: replace the
    /// `relayConnectionState != .connected` term with `true` (the cycle-local
    /// flag's behaviour for a deliberately cancelled cycle). Observed: this
    /// test fails on the confirmed-link row. Before this test existed the same
    /// mutation was green across the whole suite — which is why it exists.
    func testADeliberateTeardownOfAConfirmedLinkIsNotAConnectionFailure() {
        sut.relayConnectionState = .connected

        XCTAssertFalse(sut.relayCycleCountsAsFailure(closeCode: .invalid),
                       "a reap or a wake cancels a healthy socket; that is not the relay failing")
    }

    func testACycleThatNeverConfirmedIsAConnectionFailure() {
        sut.relayConnectionState = .connecting

        XCTAssertTrue(sut.relayCycleCountsAsFailure(closeCode: .invalid),
                      "a cycle that never got a byte back is what the #846 streak counts")
    }

    /// 4401 is a rejected token, not a wedged connection: `relayConnect()`
    /// parks the link and stops reconnecting, so it must not also spend a slot
    /// on the recycle streak.
    func testATokenRejectionIsNotAConnectionFailure() {
        sut.relayConnectionState = .connecting

        XCTAssertFalse(
            sut.relayCycleCountsAsFailure(closeCode: .init(rawValue: 4401)!),
            "a 4401 is an auth problem, not an unreachable relay")
    }

    func testRecordFailedRelayConnectAttemptDoesNotRecycleBeforeThreshold() {
        let originalSession = sut.relayURLSession

        for _ in 0..<(sut.relayConnectFailuresBeforeSessionRecycle - 1) {
            XCTAssertFalse(sut.recordFailedRelayConnectAttempt())
        }

        XCTAssertFalse(sut.relayConnectionStalled, "must not surface stalled before the threshold")
        XCTAssertTrue(sut.relayURLSession === originalSession, "session must survive isolated blips")
    }

    func testRecordFailedRelayConnectAttemptRecyclesSessionAtThreshold() {
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

    func testResetRelayConnectBackoffClearsStaleFailureState() {
        sut.relayReconnectDelay = 8.0
        sut.consecutiveRelayConnectFailures = sut.relayConnectFailuresBeforeSessionRecycle
        sut.relayConnectionStalled = true

        sut.resetRelayConnectBackoff()

        XCTAssertEqual(sut.relayReconnectDelay, 1.0)
        XCTAssertEqual(sut.consecutiveRelayConnectFailures, 0)
        XCTAssertFalse(sut.relayConnectionStalled)
    }

    func testStopRelayClearsStalledFlag() {
        sut.relayConnectionStalled = true

        sut.stopRelay()

        XCTAssertFalse(sut.relayConnectionStalled)
    }
}
