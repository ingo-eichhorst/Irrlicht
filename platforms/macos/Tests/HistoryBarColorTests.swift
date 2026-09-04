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
///   - any other unrecognised name — pinned when this was written as DEFENSIVE
///     ONLY, on the reasoning that `historyPriorityToState` was a 4-way switch
///     over a 2-bit code returning only ready/working/waiting/"". #1805 widened
///     the code to a whole byte, so that switch now also returns "error", and
///     the branch stays unreachable for the same structural reason: a name this
///     build cannot map still arrives as a CODE, and an unknown code decodes to
///     "". The row's state ICON renders neutral grey for it, so a green bar
///     beside it would make one row tell two stories.
///
/// #1805 — WHY THIS FILE GAINED A FOURTH BUCKET, DELIBERATELY.
///
/// The waiver in `tools/state-vocabulary-lint.waivers` described this suite as
/// "a LOCK on those three canonical buckets — it must NOT silently gain a
/// fourth". The operative word was *silently*. `error` had no wire code to
/// arrive on, so a fourth assertion here would have pinned a colour no bucket
/// could ever decode to — green paint on a door that opened onto a wall. #1805
/// gave it code 3, so the fourth bucket is now reachable and asserting it is
/// the point rather than a loosening. The decode half it depends on lives in
/// `HistoryWireFormatTests`.
final class HistoryBarColorTests: XCTestCase {
    func testKnownStatesKeepTheirOwnColors() {
        // LOCK: the canonical buckets must not move. `error` joined them in
        // #1805 — see the type comment for why that is a deliberate change to
        // this lock and not an erosion of it.
        XCTAssertEqual(HistoryBarView.barColor(for: "working"), IrrColors.working)
        XCTAssertEqual(HistoryBarView.barColor(for: "waiting"), IrrColors.waiting)
        XCTAssertEqual(HistoryBarView.barColor(for: "ready"), IrrColors.ready)
        XCTAssertEqual(HistoryBarView.barColor(for: "error"), IrrColors.error)
    }

    /// The strip and the row's state icon are the same red. `barColor` derives
    /// from `SessionState.State`, so this holds by construction — which is
    /// exactly why the macOS side needed no colour-table edit for #1805, while
    /// the web side did (`irrlicht.js` hardcodes its palette to stay off
    /// computed styles at canvas-paint time).
    func testErrorBarAgreesWithTheRowsStateIcon() {
        XCTAssertEqual(HistoryBarView.barColor(for: "error"), SessionState.State.error.color)
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
