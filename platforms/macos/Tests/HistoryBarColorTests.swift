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
///   - `""`, the no-data wire code. `historyPriorityToState` returns it for
///     code 3 and its own doc comment says "HistoryBarView treats it as a blank
///     slot" — which was false. `trimLeadingNoData` strips only LEADING
///     empties, so a gap anywhere else in the ring painted a solid green bucket,
///     inventing a finished session out of missing data. The web twin has always
///     been right here (`irrlicht.js`: `if (!color) continue`), so before this
///     fix the two platforms rendered the same ring differently.
///   - any other unrecognised name — a state written by a newer daemon. After
///     #1797 the row's state ICON renders neutral grey for these, so leaving the
///     bar green made a single row contradict itself.
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

    func testUnrecognizedStatePaintsNeutralNotGreen() {
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
