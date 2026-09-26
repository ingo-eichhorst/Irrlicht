import SwiftUI
import XCTest
@testable import Irrlicht

/// Issue #2063: the header capped quota chips at two, so a third provider
/// (Meta, since #2058) pushed OpenAI into a "+1 more" pill. The split and the
/// density tier are a pure function of the provider count, asserted here for
/// every count the header distinguishes. `quotaChips.test.js` asserts the same
/// table for the web dashboard's `quotaChipLayout`.
///
/// Density is compared by `rawValue` so a missing tier fails an assertion
/// rather than the compile.
@MainActor
final class QuotaChipLayoutTests: XCTestCase {

    private func assertLayout(_ count: Int, visible: Int, overflow: Int, density: String,
                              file: StaticString = #filePath, line: UInt = #line) {
        let layout = QuotaChipLayout.forChipCount(count)
        XCTAssertEqual(layout.visibleCount, visible, "visible chips for \(count) providers", file: file, line: line)
        XCTAssertEqual(layout.overflowCount, overflow, "overflow count for \(count) providers", file: file, line: line)
        XCTAssertEqual(layout.density.rawValue, density, "density for \(count) providers", file: file, line: line)
    }

    // MARK: - Lock: one and two providers render as before #2063

    func testOneProviderKeepsTheRegularTier() {
        assertLayout(1, visible: 1, overflow: 0, density: "regular")
    }

    func testTwoProvidersKeepTheCompactTier() {
        assertLayout(2, visible: 2, overflow: 0, density: "compact")
    }

    // MARK: - Red-first: three to five providers all render inline

    func testThreeProvidersRenderInlineAtTheTightTier() {
        assertLayout(3, visible: 3, overflow: 0, density: "tight")
    }

    func testFourProvidersRenderInlineAtTheDenseTier() {
        assertLayout(4, visible: 4, overflow: 0, density: "dense")
    }

    func testFiveProvidersRenderInlineAtTheDenseTier() {
        assertLayout(5, visible: 5, overflow: 0, density: "dense")
    }

    // MARK: - More than five falls back to the overflow pill

    /// Past five providers the pill takes a chip's slot: five dense chips
    /// plus the pill overrun the header (see
    /// `testTheWorstCaseRowFitsTheHeaderBudget`), four plus the pill do not.
    func testSixProvidersShowFourInlineAndTwoInTheOverflowPill() {
        assertLayout(6, visible: 4, overflow: 2, density: "dense")
    }

    func testSevenProvidersShowFourInlineAndThreeInTheOverflowPill() {
        assertLayout(7, visible: 4, overflow: 3, density: "dense")
    }

    func testNoProvidersRenderNothing() {
        let layout = QuotaChipLayout.forChipCount(0)
        XCTAssertEqual(layout.visibleCount, 0)
        XCTAssertEqual(layout.overflowCount, 0)
    }

    // MARK: - Lock: the fixed tiers keep the numbers they had before #2063

    func testTheFixedTiersKeepTheirPreviousWidths() {
        XCTAssertEqual(QuotaChipDensity.regular.fixedBarWidth, 70)
        XCTAssertEqual(QuotaChipDensity.compact.fixedBarWidth, 60)
        for d in [QuotaChipDensity.regular, .compact] {
            XCTAssertEqual(d.percentWidth, 28, "\(d)")
            XCTAssertEqual(d.rowSpacing, 6, "\(d)")
            XCTAssertEqual(d.iconSpacing, 6, "\(d)")
            XCTAssertEqual(d.chipSpacing, 8, "\(d)")
            XCTAssertEqual(d.usageMinWidth, 88, "\(d)")
            XCTAssertTrue(d.showsWindowLabel, "\(d)")
            XCTAssertTrue(d.usageShowsSublabel, "\(d)")
        }
        XCTAssertTrue(QuotaChipDensity.regular.showsResetLabel)
        XCTAssertFalse(QuotaChipDensity.compact.showsResetLabel)
    }

    // MARK: - The flexible tiers fit the 380pt header

    /// The narrowest a view lays out when offered almost no width.
    private func minimumWidth(_ view: some View) -> CGFloat {
        NSHostingController(rootView: view).sizeThatFits(in: CGSize(width: 1, height: 100)).width
    }

    private func idealWidth(_ view: some View) -> CGFloat {
        NSHostingController(rootView: view).sizeThatFits(in: CGSize(width: 10_000, height: 100)).width
    }

    private let window = RateLimitWindowInfo(usedPercent: 100, windowMinutes: 300,
                                             resetsAt: PinnedNowSnapshot.referenceNow.addingTimeInterval(3_600))

    /// Width left for the chip strip in the popover header: the panel minus
    /// its horizontal padding, the Spacer's minimum and the right-hand
    /// controls. The controls are an ESTIMATE, not a measurement of
    /// `SessionListView` (its header is private and needs a live
    /// SessionManager): a replica of the widest mode button (history mode,
    /// clock glyph + "60") is measured, and the collapse button (16pt frame),
    /// status dot (6pt) and three 8pt HStack gaps are the frames
    /// `sessionHeaderView` declares.
    private func chipBudget() -> CGFloat {
        let modeButton = idealWidth(HStack(spacing: 1) {
            Image(systemName: "clock")
            Text("60").font(.system(size: 9, weight: .semibold, design: .monospaced))
        }.font(.system(size: 11)).frame(minWidth: 16))
        let controls = modeButton + 16 + 6 + 3 * 8
        let spacerMin: CGFloat = 8
        return SessionListView.panelWidth - 2 * IrrSpacing.sp3 - spacerMin - controls
    }

    /// The narrowest a row of `count` subscription chips gets: each chip's
    /// 14pt icon, its icon gap and its narrowest window row (measured), the
    /// gaps between chips, and the "+N more" pill when there is one.
    private func narrowestStrip(count: Int) -> CGFloat {
        let layout = QuotaChipLayout.forChipCount(count)
        let d = layout.density
        let row = minimumWidth(QuotaWindowRow(window: window, density: d))
        var width = CGFloat(layout.visibleCount) * (14 + d.iconSpacing + row)
            + CGFloat(max(0, layout.visibleCount - 1)) * d.chipSpacing
        if layout.overflowCount > 0 {
            width += d.chipSpacing + idealWidth(QuotaOverflowPill(hiddenCount: layout.overflowCount))
        }
        return width
    }

    func testTheWorstCaseRowFitsTheHeaderBudget() {
        let budget = chipBudget()
        // A budget this small means the replica failed to measure, not that
        // the header shrank — fail rather than grade chips against nonsense.
        XCTAssertGreaterThan(budget, 200, "the header budget could not be measured")
        XCTAssertLessThan(budget, 330, "the header budget could not be measured")
        for count in 3...7 {
            let strip = narrowestStrip(count: count)
            print("quota strip, \(count) providers: narrowest \(strip)pt of a \(budget)pt budget")
            XCTAssertLessThanOrEqual(strip, budget,
                                     "\(count) providers overrun the header even with every bar at its minimum")
        }
    }

    /// The flexible row's minimum must actually be its bar's minimum plus the
    /// fixed parts — if the bar stopped flexing (a fixed frame put back), the
    /// "narrowest" width above would silently be its ideal width.
    func testTheFlexibleRowsShrinkBelowTheirIdealWidth() {
        for d in [QuotaChipDensity.tight, .dense] {
            let row = QuotaWindowRow(window: window, density: d)
            XCTAssertLessThan(minimumWidth(row), idealWidth(row), "\(d) bars must flex")
        }
    }
}
