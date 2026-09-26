import SwiftUI
import XCTest
@testable import Irrlicht

/// Issue #2063: with three providers the header's "+1 more" pill rendered as
/// an empty grey box. The chips beside it are fixed-width, so when the row ran
/// out of room the pill's `Text` was the only child that could give width back
/// and SwiftUI truncated it down to nothing.
///
/// A width, not a pixel, is the instrument (the same reasoning as
/// `MenuBarSlotBudgetStepperTests`): the pill is laid out beside a fixed-width
/// block that already fills an over-constrained row, and its laid-out width must
/// equal the width it wants on its own. Seen red before the fix: the
/// compressed pill reported a width smaller than its unconstrained one.
@MainActor
final class QuotaOverflowPillTests: XCTestCase {

    /// Reports the width the pill was actually given inside `CrowdedRow`.
    private final class WidthBox { var width: CGFloat? }

    private struct CrowdedRow: View {
        let box: WidthBox
        var body: some View {
            HStack(spacing: 8) {
                // Stands in for chips whose rows carry fixed frames — they
                // cannot shrink, so they cannot absorb the shortfall.
                Color.gray.frame(width: 300, height: 20)
                QuotaOverflowPill(hiddenCount: 3)
                    .background(GeometryReader { geo in
                        Color.clear
                            .onAppear { box.width = geo.size.width }
                            .onChange(of: geo.size.width) { _, w in box.width = w }
                    })
            }
            .frame(width: 320, height: 24, alignment: .leading)
        }
    }

    private func unconstrainedWidth() -> CGFloat {
        PinnedSnapshotHost(QuotaOverflowPill(hiddenCount: 3), width: 200, height: 40).view.fittingSize.width
    }

    /// Polls for the geometry report to a deadline rather than sleeping: a
    /// report that never arrives must fail loudly, not read as zero.
    private func crowdedWidth() -> CGFloat? {
        let box = WidthBox()
        let host = PinnedSnapshotHost(CrowdedRow(box: box), width: 320, height: 24)
        let deadline = Date().addingTimeInterval(2)
        while box.width == nil && Date() < deadline {
            host.view.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
        }
        return box.width
    }

    func testThePillKeepsItsFullWidthInAnOverfullRow() throws {
        let wanted = unconstrainedWidth()
        // "+3 more" at 10pt monospaced medium is ~43pt of text alone; a pill
        // narrower than that cannot be showing it.
        XCTAssertGreaterThan(wanted, 43, "the unconstrained pill must be wide enough to hold its label")
        let given = try XCTUnwrap(crowdedWidth(), "the pill never reported a laid-out width — this check cannot have run")
        print("overflow pill: wanted \(wanted)pt, given \(given)pt in an overfull row")
        XCTAssertEqual(given, wanted, accuracy: 0.5,
                       "the \"+N more\" pill was compressed in a crowded header — its label truncates to an empty box")
    }
}
