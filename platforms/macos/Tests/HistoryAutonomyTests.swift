import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Autonomy section (#1905), redesigned: five per-project panels, each a
/// longest-run line over a concurrency histogram.
///
/// What these pin is the set of claims the panels make about data they cannot
/// show in full — the gap rule on BOTH marks, the shared axis that makes five
/// panels comparable, the split that is only ever shown where the data supports
/// one, and the decode contract that keeps an older daemon's payload readable.
final class HistoryAutonomyTests: XCTestCase {

    private let utc = TimeZone(identifier: "UTC")!

    // MARK: Decoding

    func testProjectsResponseDecodes() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":0,"end":259200,"bucket_seconds":86400,
         "bucket_starts":[0,86400,172800],
         "panels":[{"project":"irrlicht","longest":41940,"longest_running":true,"total_seconds":90000,
                    "runs":12,"peak":5,"peak_top":3,"peak_sub":2,"peak_split":true,
                    "buckets":[{"ts":0,"longest":41940,"running":true,"peak":5,"peak_top":3,"peak_sub":2,
                                "peak_split":true},
                               {"ts":172800,"longest":120,"peak":1,"peak_top":1,"peak_sub":0,
                                "peak_split":true}]}],
         "panel_limit":5,"more_projects":7,
         "summary":{"longest":41940,"longest_running":true,"longest_project":"irrlicht","peak":5,
                    "peak_project":"irrlicht","runs":12,"projects":8},
         "earliest_span":1700000000,"total_recorded":33}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertTrue(d.hasData)
        XCTAssertEqual(d.panels.count, 1)
        XCTAssertEqual(d.panels[0].peakSub, 2)
        XCTAssertTrue(d.panels[0].longestRunning)
        XCTAssertEqual(d.moreProjects, 7)
        XCTAssertEqual(d.summary.longestProject, "irrlicht")
        XCTAssertEqual(d.summary.projects, 8)
    }

    /// A payload from a daemon that predates a field must still decode: a
    /// client that goes blank against an older daemon is a worse failure than
    /// one that says nothing about the field.
    func testOptionalFieldsDecodeAsSayingNothing() throws {
        let json = """
        {"window":"1y","chart":"autonomy_projects","start":0,"end":1,"bucket_seconds":604800,
         "bucket_starts":[0],"panels":[{"project":"p","longest":60,"buckets":[{"ts":0,"longest":60}]}],
         "panel_limit":5,"more_projects":0,
         "summary":{"longest":60},"earliest_span":0,"total_recorded":0}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.provenanceOrNone, .none)
        XCTAssertEqual(d.measurementOrNone, .none)
        XCTAssertNil(d.kinds)
        XCTAssertFalse(d.panels[0].longestRunning)
        XCTAssertEqual(d.panels[0].buckets[0].peak, 0)
        XCTAssertFalse(d.panels[0].buckets[0].peakSplit)
    }

    func testEmptyPanelListIsNoData() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":0,"end":1,"bucket_seconds":86400,
         "bucket_starts":[0],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0},"earliest_span":1700000000,"total_recorded":42}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertFalse(d.hasData, "no panels means no data, even with a non-zero total on record")
        XCTAssertEqual(d.totalRecorded, 42, "…and the total is what keeps that from reading as \"you did nothing\"")
    }

    // MARK: A bucket with no runs is a gap, not a zero

    /// The honesty rule, and it now binds TWO marks rather than one: the line
    /// has to break, and the histogram has to draw nothing. A zero-height bar
    /// sitting on the axis looks measured; a zero point pulls the line down.
    func testAlignedBucketsPutTheGapBack() throws {
        let d = try response(bucketStarts: [100, 200, 300, 400, 500],
                             panels: [("p", [(100, 60.0, 1), (400, 90.0, 2)])])
        let points = d.panels[0].alignedBuckets(d.bucketStarts)
        XCTAssertEqual(points.count, 5)
        XCTAssertEqual(points[0]?.longest, 60)
        XCTAssertNil(points[1])
        XCTAssertNil(points[2])
        XCTAssertEqual(points[3]?.longest, 90)
        XCTAssertNil(points[4])
    }

    /// The committed mutation: `buckets` read straight off the payload — a
    /// dense list in which the gap is simply not there, and whatever draws it
    /// runs the stroke straight across.
    func testProductionTellsAGappedRangeFromAnUnbrokenOne() throws {
        let d = try response(bucketStarts: [100, 200, 300, 400, 500],
                             panels: [("p", [(100, 60.0, 1), (400, 90.0, 2)])])
        let dense = d.panels[0].buckets.map { Optional($0) }
        XCTAssertEqual(AutonomyLineLayout.segments(points: dense),
                       [AutonomyLineLayout.Segment(from: 0, to: 1)],
                       "the dense mutation draws one unbroken stretch")
        XCTAssertEqual(AutonomyLineLayout.segments(points: d.panels[0].alignedBuckets(d.bucketStarts)),
                       [AutonomyLineLayout.Segment(from: 0, to: 0),
                        AutonomyLineLayout.Segment(from: 3, to: 3)],
                       "production breaks at the gap")
    }

    func testABarIsDrawnOnlyWhereSomebodyWasWorking() throws {
        let d = try response(bucketStarts: [100, 200],
                             panels: [("p", [(100, 60.0, 2), (200, 90.0, 0)])])
        let points = d.panels[0].alignedBuckets(d.bucketStarts)
        XCTAssertTrue(points[0]?.hasBar == true)
        XCTAssertFalse(points[1]?.hasBar == true,
                       "a zero-height bar on the axis reads as a measured zero, which is a different " +
                       "and false claim from \"nobody was working\"")
    }

    /// A bucket a long run merely crossed: something WAS working, nothing
    /// finished. Two true facts, and neither may be borrowed for the other.
    func testARunThatPassedThroughBreaksTheLineButKeepsItsBar() throws {
        let d = try response(bucketStarts: [100, 200, 300],
                             panels: [("p", [(100, 60.0, 1), (200, 0.0, 3), (300, 90.0, 1)])])
        let points = d.panels[0].alignedBuckets(d.bucketStarts)
        XCTAssertEqual(AutonomyLineLayout.segments(points: points),
                       [AutonomyLineLayout.Segment(from: 0, to: 0),
                        AutonomyLineLayout.Segment(from: 2, to: 2)])
        XCTAssertEqual(points[1]?.peak, 3)
        XCTAssertFalse(points[1]?.hasLine == true)
    }

    func testAnEmptyAxisProducesNoSegments() {
        XCTAssertTrue(AutonomyLineLayout.segments(points: []).isEmpty)
        XCTAssertTrue(AutonomyLineLayout.segments(points: [nil, nil]).isEmpty)
    }

    func testAnIsolatedBucketIsAZeroWidthSegment() throws {
        let d = try response(bucketStarts: [0, 100, 200], panels: [("p", [(100, 60.0, 1)])])
        let segments = AutonomyLineLayout.segments(points: d.panels[0].alignedBuckets(d.bucketStarts))
        XCTAssertEqual(segments, [AutonomyLineLayout.Segment(from: 1, to: 1)])
        XCTAssertTrue(segments[0].isIsolated, "it has no neighbour to draw a segment to, so it is drawn as a point")
    }

    // MARK: The panel's own LINEAR axis

    /// ITS OWN, because exactly one panel is drawn: there is nothing left to be
    /// comparable WITH, and a domain stretched to fit projects that are not on
    /// screen would flatten the one that is.
    ///
    /// The committed mutation is `sharedUpper` — the shared domain the
    /// five-panel stack used. Under it `small`'s 120s is plotted against
    /// `big`'s 11h39m and is a flat line on the floor.
    func testTheYDomainIsTheDrawnPanelsOwn() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("big", [(0, 41_940.0, 5)]), ("small", [(0, 120.0, 1)])])
        let own = try XCTUnwrap(d.panels[1].yDomain)
        XCTAssertEqual(own.upperBound, 120 * 1.1, accuracy: 0.001)

        let sharedUpper = d.panels.compactMap { $0.buckets.map(\.longest).max() }.max() ?? 0
        XCTAssertGreaterThan(sharedUpper, own.upperBound,
                             "a shared domain would put the small project's whole plot on the floor")
    }

    /// LINEAR, not logarithmic. The maintainer's explicit call (#1905), and the
    /// committed mutation is the log mapping that shipped before: halfway up a
    /// LINEAR plot is half the domain, where on a log plot it is the geometric
    /// mean.
    func testTheAxisIsLinearAndStartsAtZero() throws {
        let d = try response(bucketStarts: [0], panels: [("p", [(0, 100.0, 1)])])
        let domain = try XCTUnwrap(d.panels[0].yDomain)
        XCTAssertEqual(domain.lowerBound, 0,
                       "a linear axis whose origin is not zero exaggerates every difference above it")
        XCTAssertEqual(domain.upperBound, 110, accuracy: 0.001)

        // The MUTATION: the log domain, floored at 1s because a log scale
        // cannot plot 0. Its lower bound is not zero, which is the difference.
        let logLower = Swift.max(1, 100.0 * 0.8)
        XCTAssertNotEqual(logLower, domain.lowerBound)
    }

    /// What linear COSTS, recorded rather than hidden: a project whose longest
    /// day is 11h39m plots its typical 10m runs in the bottom ~1.5% of the plot.
    /// The panel header states the exact longest as a NUMBER for this reason.
    func testTheCostOfALinearAxisIsRecorded() throws {
        let d = try response(bucketStarts: [0], panels: [("big", [(0, 41_940.0, 5)])])
        let domain = try XCTUnwrap(d.panels[0].yDomain)
        let fraction = 600 / domain.upperBound
        XCTAssertLessThan(fraction, 0.02, "10m of an 11h39m domain is under 2% of the plot height")
    }

    func testAnEmptyPanelHasNoDomainRatherThanAFabricatedOne() throws {
        let none = try response(bucketStarts: [0], panels: [])
        XCTAssertTrue(none.panels.isEmpty)
        // …and a panel whose only bucket has a bar but no finished run.
        let barsOnly = try response(bucketStarts: [0], panels: [("p", [(0, 0.0, 2)])])
        XCTAssertNil(barsOnly.panels[0].yDomain)
        XCTAssertEqual(barsOnly.panels[0].peakScale, 2)
    }

    func testThePeakScaleIsThePanelsOwn() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("a", [(0, 60.0, 5)]), ("b", [(0, 60.0, 2)])])
        XCTAssertEqual(d.panels[0].peakScale, 5)
        XCTAssertEqual(d.panels[1].peakScale, 2, "the drawn panel's own peak, not the other's")
        let flat = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 0)])])
        XCTAssertEqual(flat.panels[0].peakScale, 0,
                       "nothing overlapped anywhere, so no bar is drawn rather than every bar full height")
    }

    // MARK: The histogram labels its own y axis (#1905)

    /// "On the bar chart below the number of agents running in parallel is not
    /// visible." The gutter beside the bars was deliberately BLANK — an
    /// `AxisMarks(values: [0])` carrying a spacer string, kept only so the bars
    /// stayed in register with the plot above.
    ///
    /// The committed mutation is `blankGutter` below: what shipped.
    func testTheHistogramAxisIsLabelledAtItsPeakAndZero() {
        XCTAssertEqual(AutonomyBarAxis.values(peak: 15), [0, 15])
        XCTAssertEqual(AutonomyBarAxis.label(15), "15")
        XCTAssertEqual(AutonomyBarAxis.label(0), "0")

        let blankGutter: [Int] = []
        XCTAssertNotEqual(AutonomyBarAxis.values(peak: 15), blankGutter,
                          "the shipped gutter carried no scale at all, which is the defect")
    }

    /// An axis labelled 0 to 0 would be furniture claiming a measurement.
    func testAPanelNobodyOverlappedInGetsNoHistogramAxis() {
        XCTAssertEqual(AutonomyBarAxis.values(peak: 0), [])
        XCTAssertEqual(AutonomyBarAxis.values(peak: -1), [])
    }

    /// QA-4 (#1905). Both axes are labelled in the SAME right-hand gutter now,
    /// and neither knows about the other: the line axis's lowest label sits at
    /// the foot of its plot and the histogram's peak label at the head of the
    /// bar band. The web build showed the consequence in a browser — "0s" and
    /// "15" overprinted into a smudge at the 3 pt gap the five-panel stack used,
    /// where the bar band carried no labels at all.
    ///
    /// The committed mutation is `beforeTheGap` below: butted together, the two
    /// labels' 9 pt boxes intersect.
    func testTheTwoAxesClearEachOtherInTheSharedGutter() {
        let font = AutonomyPanelMetrics.tickFontSize
        let gap = AutonomyPanelMetrics.chartGap
        // Each label is drawn centred on its own line, so half a glyph hangs
        // off each side; clearing one whole font is what keeps them apart.
        XCTAssertGreaterThanOrEqual(gap, font,
                                    "the line axis's floor label and the histogram's peak label overprint")

        let beforeTheGap: CGFloat = 1
        XCTAssertLessThan(beforeTheGap, font,
                          "the shipped spacing did not overprint, so this check cannot show the fix")
    }

    // MARK: The concurrency figure, and the split it may not invent

    func testADerivableSplitIsSpelledOutBesideTheTotal() {
        XCTAssertEqual(AutonomyFormat.concurrency(peak: 5, top: 3, sub: 2, splitKnown: true),
                       "5 at once (3 + 2 sub)")
    }

    /// Most rows before 18 Aug 2026 carry `kind: unknown`; there is no way to
    /// recover which they were, and "5 (5 + 0 sub)" would be an invented answer.
    func testAnUnderivableSplitShowsTheTotalAlone() {
        let label = AutonomyFormat.concurrency(peak: 5, top: 0, sub: 0, splitKnown: false)
        XCTAssertEqual(label, "5 at once")
        XCTAssertFalse(label.contains("sub"))
    }

    func testAWindowNobodyOverlappedInSaysNothingAtAll() {
        XCTAssertEqual(AutonomyFormat.concurrency(peak: 0, top: 0, sub: 0, splitKnown: true), "")
    }

    /// The number is otherwise misread: a parent is held `working` while its
    /// subagents run, so one agent with three subagents reads as four at once.
    func testTheCaveatSaysWhatTheNumberIsNot() {
        let caveat = AutonomyFormat.concurrencyCaveat
        XCTAssertTrue(caveat.contains("parent"), "got: \(caveat)")
        XCTAssertTrue(caveat.contains("subagents"), "got: \(caveat)")
        XCTAssertTrue(caveat.contains("not four independent agents"), "got: \(caveat)")
    }

    // MARK: A panel states its own two figures

    func testPanelHeadlineCarriesLongestAndAtOnce() {
        let panel = HistoryAutonomyPanel(project: "irrlicht", longest: 41_940,
                                         peak: 5, peakTop: 3, peakSub: 2, peakSplit: true)
        XCTAssertEqual(panel.headline, "longest 11h39m · 5 at once (3 + 2 sub)")
    }

    func testAStillRunningLongestRunIsMarkedNotDropped() {
        let panel = HistoryAutonomyPanel(project: "p", longest: 10_800, longestRunning: true,
                                         peak: 1, peakSplit: false)
        XCTAssertTrue(panel.headline.contains("longest 3h"), "got: \(panel.headline)")
        XCTAssertTrue(panel.headline.contains("still going"), "got: \(panel.headline)")
    }

    func testAProjectNobodyOverlappedInStillStatesItsLongest() {
        XCTAssertEqual(HistoryAutonomyPanel(project: "p", longest: 600).headline, "longest 10m")
    }

    // MARK: One panel at a time, and the picker that chooses it (#1905)

    func testNothingSelectedDrawsRankOne() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("irrlicht", [(0, 600.0, 2)]), ("articles", [(0, 300.0, 1)])])
        let choice = d.choice(selected: nil)
        XCTAssertEqual(choice.project, "irrlicht")
        XCTAssertEqual(choice.panel?.project, "irrlicht")
        XCTAssertFalse(choice.isMissing)
    }

    func testASelectionThisRangeHoldsDrawsThatProject() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("irrlicht", [(0, 600.0, 2)]), ("articles", [(0, 300.0, 1)])])
        let choice = d.choice(selected: "articles")
        XCTAssertEqual(choice.panel?.project, "articles")
        XCTAssertFalse(choice.isMissing)
    }

    /// The committed mutation is `silentFallback` — `first(where:) ?? first`,
    /// the silent fall back to rank 1. Under it a Range change looks like a
    /// click the reader never made, and the project they were looking at is
    /// gone with nothing on screen saying where it went.
    func testASelectionThisRangeDoesNotHoldIsKeptAndMarked() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("irrlicht", [(0, 600.0, 2)]), ("articles", [(0, 300.0, 1)])])
        let silentFallback = d.panels.first { $0.project == "besenkammer" } ?? d.panels[0]
        XCTAssertEqual(silentFallback.project, "irrlicht")

        let choice = d.choice(selected: "besenkammer")
        XCTAssertEqual(choice.project, "besenkammer")
        XCTAssertNil(choice.panel)
        XCTAssertTrue(choice.isMissing)
        XCTAssertEqual(AutonomyFormat.emptyProjectNote("besenkammer"),
                       "no runs for besenkammer in this range")
    }

    func testAnEmptyWindowChoosesNothingRatherThanInventingAProject() throws {
        let d = try response(bucketStarts: [0], panels: [])
        XCTAssertNil(d.choice(selected: nil).panel)
        XCTAssertEqual(d.choice(selected: nil).project, "")
        XCTAssertTrue(d.projectOptions(selected: nil).isEmpty)
    }

    /// The committed mutation: a client that re-decides the list itself. Two
    /// surfaces doing that is exactly how the run strip ended up drawing twelve
    /// rows on the web against six here, from one ranked list — and a picker
    /// that caps its own list drops projects the summary beside it still counts.
    func testThePickerOffersWhatItWasSentNeverItsOwnSlice() throws {
        let d = try response(bucketStarts: [0],
                             panels: (0..<40).map { ("p\($0)", [(Int64(0), 60.0, 1)]) },
                             moreProjects: 0)
        let clientCap = Array(d.panels.prefix(5))
        XCTAssertEqual(clientCap.count, 5)
        XCTAssertEqual(d.projectOptions(selected: nil).count, d.panels.count)
        XCTAssertEqual(d.projectOptions(selected: nil).count, 40)
    }

    /// Last by the SAME rule the rest follow — the order is most autonomous
    /// time first, and a project with no runs in this range has none of it.
    func testAnAbsentSelectionStaysInThePickerLastAndLabelled() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("irrlicht", [(0, 600.0, 2)]), ("articles", [(0, 300.0, 1)])])
        let options = d.projectOptions(selected: "besenkammer")
        XCTAssertEqual(options.map(\.project), ["irrlicht", "articles", "besenkammer"])
        XCTAssertTrue(options[2].isMissing)
        XCTAssertTrue(options[2].label.contains("no runs in this range"), "got: \(options[2].label)")
        XCTAssertEqual(options[0].label, "irrlicht", "a present project is named plainly")
    }

    // MARK: What the payload cap left out

    func testTheOverflowLineNamesWhatTheCapLeftOut() throws {
        let d = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 1)])], moreProjects: 7)
        let label = try XCTUnwrap(d.overflowLabel)
        XCTAssertTrue(label.hasPrefix("+7 more projects past the payload cap"), "got: \(label)")
        XCTAssertTrue(label.contains("less autonomous time"), "got: \(label)")
    }

    func testNothingIsSaidWhenTheCapLeftNothingOut() throws {
        let d = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 1)])], moreProjects: 0)
        XCTAssertNil(d.overflowLabel)
    }

    func testOneHiddenProjectIsSingular() throws {
        let d = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 1)])], moreProjects: 1)
        XCTAssertTrue(try XCTUnwrap(d.overflowLabel).hasPrefix("+1 more project past"))
    }

    // MARK: The boundary caption reads once across the section

    /// The rule is drawn on BOTH elements — it has to be, or it annotates the
    /// aggregate line and leaves the panel below it stepping for no stated
    /// reason — but the caption is written by exactly one.
    func testTheCaptionIsWrittenOnceNotOncePerElement() {
        let carrying = AutonomyBoundaryCaption.Element.allCases
            .filter { AutonomyBoundaryCaption.isShown(element: $0) }
        XCTAssertEqual(carrying.count, 1)
        XCTAssertEqual(carrying, [AutonomyBoundaryCaption.captionedBy])
        XCTAssertTrue(AutonomyBoundaryCaption.isShown(element: .aggregate),
                      "the top element carries the words")
        XCTAssertFalse(AutonomyBoundaryCaption.isShown(element: .panel))
    }

    /// The committed mutation: captioning every element. It passes "a caption
    /// is drawn" and fails the thing that matters, which is that there is one.
    func testProductionTellsOneCaptionFromTwo() {
        let captionEverywhere: (AutonomyBoundaryCaption.Element) -> Bool = { _ in true }
        XCTAssertEqual(AutonomyBoundaryCaption.Element.allCases.filter(captionEverywhere).count, 2)
        XCTAssertEqual(AutonomyBoundaryCaption.Element.allCases
            .filter { AutonomyBoundaryCaption.isShown(element: $0) }.count, 1)
    }

    /// The caption hangs off whichever side of its rule has room. Pinned to the
    /// left unconditionally it would be clamped to the plot's leading edge for
    /// any rule in the left third, leaving the caption detached with its arrow
    /// pointing off-chart at nothing.
    func testTheCaptionHangsOffWhicheverSideOfTheRuleHasRoom() {
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.1), .right)
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.9), .left)
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.5), .left)
    }

    func testProductionTellsALeftRuleFromARightOne() {
        let alwaysLeft: (Double) -> AutonomyBoundaryCaption.Side = { _ in .left }
        XCTAssertEqual(alwaysLeft(0.1), alwaysLeft(0.9))
        XCTAssertNotEqual(AutonomyBoundaryCaption.side(fraction: 0.1),
                          AutonomyBoundaryCaption.side(fraction: 0.9))
    }

    // MARK: The window vocabulary

    /// The trap #1905 calls out by name: chart=state's granularity keys and the
    /// Autonomy windows OVERLAP textually and mean different things.
    func testAutonomyWindowsAreNotGranularities() {
        let autonomy = Set(HistoryAutonomyRange.allCases.map(\.rawValue))
        XCTAssertEqual(autonomy, ["30d", "1y"])
        // The run strip's own vocabulary went with the strip. If any of its
        // keys reappears here, two textually-overlapping sets are back on one
        // screen — which is the confusion the redesign removed.
        for stripKey in ["8h", "24h", "7d", "12mo"] {
            XCTAssertFalse(autonomy.contains(stripKey),
                           "\(stripKey) is a granularity key and must not be an Autonomy window")
        }
    }

    // MARK: The palette and the key

    /// ONE HUE, FOUR WEIGHTS: every quieter mark is the line's hue at a lower
    /// alpha. What never came back is a colour per percentile — three equally
    /// loud curves and a legend that had to be decoded before the chart said
    /// anything.
    @MainActor
    func testMarksAreOneHue() {
        for appearance in [NSAppearance(named: .aqua)!, NSAppearance(named: .darkAqua)!] {
            let line = rgb(AutonomyPalette.lineColor, appearance)
            for (name, color) in [("bars", AutonomyPalette.bars),
                                  ("band", AutonomyPalette.band),
                                  ("bandThin", AutonomyPalette.bandThin),
                                  ("edge", AutonomyPalette.edge)] {
                let mark = rgb(color, appearance)
                let where_ = "\(appearance.name.rawValue)/\(name)"
                XCTAssertEqual(line.r, mark.r, accuracy: 0.02, "\(where_): red")
                XCTAssertEqual(line.g, mark.g, accuracy: 0.02, "\(where_): green")
                XCTAssertEqual(line.b, mark.b, accuracy: 0.02, "\(where_): blue")
                XCTAssertLessThan(mark.a, line.a,
                                  "\(where_): a second reading at the line's full strength competes " +
                                  "with the line it belongs to")
            }
        }
    }

    /// The thin plane must be FAINTER than the ordinary one, or the marking
    /// that says "these are not percentiles" says nothing at all.
    @MainActor
    func testTheThinPlaneIsFainterThanTheOrdinaryOne() {
        for appearance in [NSAppearance(named: .aqua)!, NSAppearance(named: .darkAqua)!] {
            XCTAssertLessThan(rgb(AutonomyPalette.bandThin, appearance).a,
                              rgb(AutonomyPalette.band, appearance).a,
                              "\(appearance.name.rawValue): a thin bucket's plane is not distinguishable")
        }
    }

    func testTheKeyHasFourEntriesOnePerMark() {
        let entries = AutonomyPalette.keyEntries
        XCTAssertEqual(entries.map(\.kind), [.p50, .band, .line, .bars])
        XCTAssertEqual(entries.map(\.id), ["p50", "band", "line", "bars"])
        // Only the band has an area; every other mark is a stroke.
        XCTAssertEqual(entries.filter { $0.fill != nil }.map(\.kind), [.band])
    }

    /// Both elements' marks are named. A key that covered only one of them
    /// would leave half the section's ink unexplained.
    func testTheKeyExplainsBothElementsInWords() {
        let labels = AutonomyPalette.keyEntries.map(\.label).joined(separator: " ")
        for named in ["p50", "p5–p95", "spread", "longest run", "at once"] {
            XCTAssertTrue(labels.contains(named), "\(named) is missing from the key: \(labels)")
        }
    }

    // MARK: Formatting

    func testDurationFormatting() {
        XCTAssertEqual(AutonomyFormat.duration(41), "41s")
        XCTAssertEqual(AutonomyFormat.duration(660), "11m")
        XCTAssertEqual(AutonomyFormat.duration(7080), "1h58m")
        XCTAssertEqual(AutonomyFormat.duration(41_940), "11h39m")
        XCTAssertEqual(AutonomyFormat.duration(86_400), "1d")
        XCTAssertEqual(AutonomyFormat.duration(0), "0s")
    }

    func testAxisBoundCoarsensWithTheWindow() {
        let d = Date(timeIntervalSince1970: 1_767_000_000) // 2025-12-29T09:20:00Z
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 8 * 3600, timeZone: utc), "09:20")
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 30 * 86400, timeZone: utc), "Dec 29")
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 365 * 86400, timeZone: utc), "Dec 2025")
    }

    /// Every date this surface renders takes its zone as an INPUT (#1659).
    func testAxisBoundHonorsTheCallersZone() {
        // 2025-12-29T23:30:00Z — still Dec 29 in UTC, already Dec 30 in Tokyo
        // (UTC+9). A date that read the same in both zones would let this pass
        // against a formatter that ignored the parameter entirely.
        let d = Date(timeIntervalSince1970: 1_767_051_000)
        let tokyo = TimeZone(identifier: "Asia/Tokyo")!
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 30 * 86400, timeZone: utc), "Dec 29")
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 30 * 86400, timeZone: tokyo), "Dec 30")
    }

    func testProvenanceLineSaysWhenCollectionStarted() {
        let empty = AutonomyFormat.provenance(earliest: 0, total: 0, timeZone: utc)
        XCTAssertTrue(empty.contains("began measuring"), "got: \(empty)")
        let seeded = AutonomyFormat.provenance(earliest: 1_755_000_000, total: 312, timeZone: utc)
        XCTAssertTrue(seeded.contains("Collecting since Aug 12, 2025"), "got: \(seeded)")
        XCTAssertTrue(seeded.contains("312 runs recorded"), "got: \(seeded)")
    }

    // MARK: Fixtures

    /// Builds a payload through the real decode path rather than a hand-built
    /// struct, so a wire-name slip fails here rather than in production.
    private func response(bucketStarts: [Int64],
                          panels: [(String, [(Int64, Double, Int)])],
                          moreProjects: Int = 0) throws -> HistoryAutonomyProjectsResponse {
        let starts = bucketStarts.map(String.init).joined(separator: ",")
        let panelJSON = panels.map { project, buckets -> String in
            let bs = buckets.map { ts, longest, peak in
                "{\"ts\":\(ts),\"longest\":\(longest),\"peak\":\(peak),\"peak_top\":\(peak)," +
                "\"peak_sub\":0,\"peak_split\":true}"
            }.joined(separator: ",")
            let longest = buckets.map(\.1).max() ?? 0
            return "{\"project\":\"\(project)\",\"longest\":\(longest),\"total_seconds\":\(longest)," +
                "\"runs\":\(buckets.count),\"peak\":\(buckets.map(\.2).max() ?? 0),\"buckets\":[\(bs)]}"
        }.joined(separator: ",")
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":0,"end":1,"bucket_seconds":86400,
         "bucket_starts":[\(starts)],"panels":[\(panelJSON)],
         "panel_limit":5,"more_projects":\(moreProjects),
         "summary":{"longest":0},"earliest_span":0,"total_recorded":0}
        """
        return try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
    }

    /// The resolved sRGB components of a token under one appearance — the same
    /// technique the theme suites use, so a token that is a literal in one
    /// appearance and adaptive in the other is still compared honestly.
    @MainActor
    private func rgb(_ color: Color,
                     _ appearance: NSAppearance,
                     file: StaticString = #filePath,
                     line: UInt = #line) -> (r: CGFloat, g: CGFloat, b: CGFloat, a: CGFloat) {
        var out: (CGFloat, CGFloat, CGFloat, CGFloat) = (0, 0, 0, 0)
        appearance.performAsCurrentDrawingAppearance {
            guard let srgb = NSColor(color).usingColorSpace(.sRGB) else {
                XCTFail("token does not resolve to an sRGB colour under \(appearance.name.rawValue)",
                        file: file, line: line)
                return
            }
            out = (srgb.redComponent, srgb.greenComponent, srgb.blueComponent, srgb.alphaComponent)
        }
        return out
    }
}
