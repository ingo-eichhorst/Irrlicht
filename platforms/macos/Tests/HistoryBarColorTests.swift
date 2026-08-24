import SwiftUI
import XCTest
@testable import Irrlicht

/// #1797 — the per-row history bar was the fourth reader that answered an
/// unrecognised bucket with READY GREEN:
///
/// ```swift
/// let color = stateColors[state] ?? stateColors["ready"]!
/// ```
///
/// Two separate inputs hit that fallback, and both mattered:
///
///   - `""`, the no-data wire code — the REACHABLE one. `historyPriorityToState`
///     returns it for code 3 and its own doc comment says "HistoryBarView treats
///     it as a blank slot", which was false. `trimLeadingNoData` strips only
///     LEADING empties, so a gap anywhere else in the ring painted a solid green
///     bucket, inventing a finished session out of missing data. The web twin
///     has always been right here (`irrlicht.js`: `if (!color) continue`), so
///     before this fix the two platforms rendered the same ring differently.
///   - any other unrecognised name — DEFENSIVE ONLY, and marked as such rather
///     than asserted: every string reaching `states` comes from
///     `historyPriorityToState`, a 4-way switch over a 2-bit code returning only
///     ready/working/waiting/"", so a newer daemon's state arrives as a priority
///     CODE and this branch cannot fire in production today. It is pinned
///     anyway because the epic will widen that encoding (#1807), and because the
///     row's state ICON already renders neutral grey — a green bar beside it
///     would make one row tell two stories.
final class HistoryBarColorTests: XCTestCase {
    func testKnownStatesKeepTheirOwnColors() {
        // LOCK: the three canonical buckets must not move.
        XCTAssertEqual(HistoryBarView.barColor(for: "working"), IrrColors.working)
        XCTAssertEqual(HistoryBarView.barColor(for: "waiting"), IrrColors.waiting)
        XCTAssertEqual(HistoryBarView.barColor(for: "ready"), IrrColors.ready)
    }

    func testNoDataBucketPaintsNothingRatherThanGreen() {
        XCTAssertNil(
            HistoryBarView.barColor(for: ""),
            "the no-data code must leave the slot unpainted, not paint it ready-green"
        )
    }

    /// Defensive branch — see the type comment: not reachable in production
    /// today, pinned for when the wire encoding widens.
    func testUnrecognizedStatePaintsNeutralNotGreen_defensive() {
        let color = HistoryBarView.barColor(for: "zzz-unknown")
        XCTAssertNotNil(color, "an unrecognized state should still render, just not as ready")
        XCTAssertNotEqual(color, IrrColors.ready, "an unrecognized state must never paint ready-green")
        XCTAssertEqual(color, IrrColors.unknown)
    }

    /// The bar and the state icon sit in the same row (`SessionRowView`), so a
    /// disagreement between them is visible to the user as one row telling two
    /// stories.
    func testBarAgreesWithTheRowsStateIconForUnknown() {
        XCTAssertEqual(HistoryBarView.barColor(for: "zzz-unknown"), SessionState.State.unknown.color)
    }
}
