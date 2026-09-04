import XCTest
@testable import Irrlicht

/// #1805 — the history strip's decode path on macOS.
///
/// Every test in this file is new, and the gap it fills is the reason the file
/// exists rather than a nice-to-have. Before #1805 the Swift decoder had NO
/// coverage at all: not `decodeHistoryBuckets`, not `historyPriorityToState`,
/// not `historyPriorityForState`, not the three apply* entry points — while
/// hardcoding a byte width, a bucket count and a bit-shift stride. The web twin
/// was in the same state. A wire-format change would therefore have gone green
/// through every gate in the repo and rendered garbage on both clients.
///
/// The vector is front-padded with the no-data sentinel and carries the ladder
/// in its last four slots — the shape `encodePriorities` actually emits for a
/// partly-filled ring. The same 60 bytes are produced by
/// `history_wire_test.go`'s `TestHistoryTracker_EncodeByteLayout` and decoded by
/// `irrlicht.history.test.js`'s `SHARED_VECTOR`. The three decoders are
/// independent hand-written copies with no shared source, so one vector asserted
/// in all three is what stops them drifting apart silently.
///
/// An earlier draft of this comment named `history_tracker_test.go` and
/// `irrlicht.test.js` — both wrong after the tests moved — and claimed the JS
/// vector matched when it was the mirror image, ladder-first. Three fixtures
/// asserted to be one vector had already diverged before they shipped, which is
/// the whole failure mode this file exists to prevent.
@MainActor
final class HistoryWireFormatTests: XCTestCase {
    private var manager: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        manager = SessionManager(defaults: InMemoryDefaults())
    }

    override func tearDown() async throws {
        manager = nil
        try await super.tearDown()
    }

    /// Base64 of an explicit byte vector, front-padded with the no-data
    /// sentinel exactly as the daemon's `encodePriorities` pads a partial ring.
    private func encoded(_ tail: [UInt8]) -> String {
        var bytes = [UInt8](repeating: 255, count: 60)
        bytes.replaceSubrange((60 - tail.count)..<60, with: tail)
        return Data(bytes).base64EncodedString()
    }

    // MARK: - the code table

    func testLadderMapsToItsFourStatesInPriorityOrder() {
        XCTAssertEqual(manager.historyPriorityToState(0), "ready")
        XCTAssertEqual(manager.historyPriorityToState(1), "working")
        XCTAssertEqual(manager.historyPriorityToState(2), "waiting")
        XCTAssertEqual(manager.historyPriorityToState(3), "error")
    }

    func testNoDataSentinelIsBlankNeverAState() {
        XCTAssertEqual(manager.historyPriorityToState(255), "")
    }

    /// A code this build cannot name must fall to blank, never onto a real
    /// state. Painting an unknown code as `ready` is the green-error bug #1807
    /// removed; after #1805 a code that folded down to 3 would paint `error`
    /// and invent a failure that never happened. Blank is the only honest
    /// answer, and `HistoryBarView.barColor(for: "")` then paints nothing.
    func testCodeFromANewerDaemonIsBlankNotFoldedOntoAState() {
        for code: UInt8 in [4, 5, 7, 8, 64, 254] {
            XCTAssertEqual(
                manager.historyPriorityToState(code), "",
                "wire code \(code) must decode blank, not onto a state this build can paint"
            )
        }
    }

    func testPriorityForStatePutsErrorOnTop() {
        XCTAssertEqual(manager.historyPriorityForState("error"), 3)
        XCTAssertEqual(manager.historyPriorityForState("waiting"), 2)
        XCTAssertEqual(manager.historyPriorityForState("working"), 1)
        XCTAssertEqual(manager.historyPriorityForState("ready"), 0)
        XCTAssertEqual(manager.historyPriorityForState(""), -1)
    }

    // MARK: - the decoder

    func testDecodesOneBytePerBucketOldestToNewest() {
        let out = manager.decodeHistoryBuckets(encoded([0, 1, 2, 3]))
        XCTAssertEqual(out?.count, 60)
        XCTAssertEqual(Array(out?.suffix(4) ?? []), ["ready", "working", "waiting", "error"])
        XCTAssertEqual(out?.first, "", "front padding must decode as no-data")
    }

    /// The length check is the compatibility boundary between daemon versions,
    /// so it is asserted rather than assumed. 15 bytes is exactly what a
    /// pre-#1805 daemon ships (60 buckets x 2 bits). Dropping the whole message
    /// leaves the strip blank; a partial decode would invent buckets.
    func testRejectsAnOlderDaemonFifteenBytePackedPayload() {
        let old = Data([UInt8](repeating: 0xFF, count: 15)).base64EncodedString()
        XCTAssertNil(manager.decodeHistoryBuckets(old))
    }

    func testRejectsAnyOtherWrongLengthAndUnparseableBase64() {
        XCTAssertNil(manager.decodeHistoryBuckets(Data([UInt8](repeating: 0, count: 59)).base64EncodedString()))
        XCTAssertNil(manager.decodeHistoryBuckets(Data([UInt8](repeating: 0, count: 61)).base64EncodedString()))
        XCTAssertNil(manager.decodeHistoryBuckets("!!! not base64 !!!"))
    }

    // MARK: - the merge

    /// `error` outranks every other state, so one error in a bucket paints the
    /// whole bucket red. This mirrors the daemon's ladder
    /// (TestHistoryTracker_ErrorOutranksEveryOtherState); the client re-derives
    /// priority by NAME, so the two orderings have to be asserted separately or
    /// they can disagree without anything failing.
    ///
    /// The occupant codes come from the production `historyPriorityForState`
    /// rather than a test-local table: a wire code IS its priority for every
    /// state on the ladder, and `testPriorityForStatePutsErrorOnTop` above
    /// already pins that function. A private reverse map here would have been a
    /// third copy of the ladder for this file to keep in step.
    func testUpgradeToErrorWinsOverEveryOtherState() {
        for occupant in ["ready", "working", "waiting"] {
            let sid = "sess-\(occupant)"
            manager.applyHistorySnapshot(
                sessionID: sid,
                history: ["1": encoded([UInt8(manager.historyPriorityForState(occupant))])],
                generations: nil
            )
            manager.applyHistoryUpgrade(sessionID: sid, priority: 3)
            XCTAssertEqual(
                manager.historyByGranularity[1]?[sid]?.last, "error",
                "error must win the open bucket over \(occupant)"
            )
        }
    }

    /// The mirror of the above: a LOWER-priority upgrade must not displace an
    /// error already recorded in the open bucket.
    func testUpgradeDoesNotDowngradeAnErroredBucket() {
        let sid = "sess-keeps-error"
        manager.applyHistorySnapshot(sessionID: sid, history: ["1": encoded([3])], generations: nil)
        manager.applyHistoryUpgrade(sessionID: sid, priority: 0) // ready
        XCTAssertEqual(manager.historyByGranularity[1]?[sid]?.last, "error")
    }
}
