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

    // MARK: The shared log axis

    /// Shared, so the projects are comparable — that is the point of picking
    /// five. Log, because a project whose best is two minutes would otherwise
    /// be a flat line under one whose best is eleven hours.
    func testTheYDomainSpansEveryPanel() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("big", [(0, 41_940.0, 5)]), ("small", [(0, 120.0, 1)])])
        let domain = try XCTUnwrap(d.sharedYDomain)
        XCTAssertLessThanOrEqual(domain.lowerBound, 120)
        XCTAssertGreaterThanOrEqual(domain.upperBound, 41_940)
    }

    /// The committed mutation: a per-panel domain. Under it `small`'s own 120s
    /// would sit at the top of its plot while `big`'s 120s sat near the bottom,
    /// and the five panels would not be comparable at all.
    func testProductionTellsASharedDomainFromAPerPanelOne() throws {
        // `small` FIRST on purpose: a mutation that reads only the first panel
        // would then produce the per-panel domain here, and this goes red too
        // rather than leaving one assertion to carry the whole rule.
        let d = try response(bucketStarts: [0],
                             panels: [("small", [(0, 120.0, 1)]), ("big", [(0, 41_940.0, 5)])])
        let shared = try XCTUnwrap(d.sharedYDomain)
        let onlySmall = try response(bucketStarts: [0], panels: [("small", [(0, 120.0, 1)])])
        let perPanel = try XCTUnwrap(onlySmall.sharedYDomain)
        XCTAssertNotEqual(shared.upperBound, perPanel.upperBound,
                          "a per-panel domain would put the small project's own maximum at the top of " +
                          "its plot, and the five panels would not be comparable")
        XCTAssertGreaterThan(shared.upperBound, perPanel.upperBound)
    }

    func testAnEmptyWindowHasNoDomainRatherThanAFabricatedOne() throws {
        let none = try response(bucketStarts: [0], panels: [])
        XCTAssertNil(none.sharedYDomain)
        // …and a window whose only bucket has a bar but no finished run.
        let barsOnly = try response(bucketStarts: [0], panels: [("p", [(0, 0.0, 2)])])
        XCTAssertNil(barsOnly.sharedYDomain)
        XCTAssertEqual(barsOnly.sharedPeakScale, 2)
    }

    func testThePeakScaleIsSharedToo() throws {
        let d = try response(bucketStarts: [0],
                             panels: [("a", [(0, 60.0, 5)]), ("b", [(0, 60.0, 2)])])
        XCTAssertEqual(d.sharedPeakScale, 5)
        let flat = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 0)])])
        XCTAssertEqual(flat.sharedPeakScale, 0,
                       "nothing overlapped anywhere, so no bar is drawn rather than every bar full height")
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

    // MARK: The stack, and what it left out

    func testTheStackDrawsThePanelsItWasSentAndNamesTheRest() throws {
        let d = try response(bucketStarts: [0],
                             panels: (0..<5).map { ("p\($0)", [(Int64(0), 60.0, 1)]) },
                             moreProjects: 7)
        let rows = d.stackRows
        XCTAssertEqual(rows.count, 6)
        guard case let .more(label) = rows[5] else {
            return XCTFail("the last stack row must be the overflow line, got \(rows[5])")
        }
        XCTAssertEqual(label, "+7 more projects, each with less autonomous time")
    }

    func testTheStackSaysNothingWhenEveryProjectHasAPanel() throws {
        let d = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 1)])], moreProjects: 0)
        XCTAssertNil(d.overflowLabel)
        XCTAssertEqual(d.stackRows.count, 1)
    }

    func testOneHiddenProjectIsSingular() throws {
        let d = try response(bucketStarts: [0], panels: [("a", [(0, 60.0, 1)])], moreProjects: 1)
        XCTAssertEqual(try XCTUnwrap(d.overflowLabel),
                       "+1 more project, each with less autonomous time")
    }

    /// The committed mutation: a client that re-decides the panel count itself.
    /// Two surfaces doing that is exactly how the run strip ended up drawing
    /// twelve rows on the web against six here, from one ranked list.
    func testTheClientRendersWhatItWasSentNeverItsOwnCount() throws {
        let d = try response(bucketStarts: [0],
                             panels: (0..<5).map { ("p\($0)", [(Int64(0), 60.0, 1)]) },
                             moreProjects: 7)
        let clientCap = Array(d.panels.prefix(3))
        XCTAssertEqual(clientCap.count, 3)
        let drawn = d.stackRows.filter { if case .panel = $0 { return true } else { return false } }
        XCTAssertEqual(drawn.count, d.panels.count)
        XCTAssertEqual(drawn.count, 5)
    }

    // MARK: The boundary caption reads once across the stack

    /// The rule runs through all five panels — it has to, or it annotates one
    /// project's line and leaves the four below it stepping for no stated
    /// reason — but the caption is drawn on exactly one.
    func testTheCaptionIsWrittenOnceNotOncePerPanel() {
        let carrying = (0..<5).filter { AutonomyBoundaryCaption.isShown(panelIndex: $0) }
        XCTAssertEqual(carrying.count, 1)
        XCTAssertEqual(carrying, [AutonomyBoundaryCaption.panelIndex])
        XCTAssertTrue(AutonomyBoundaryCaption.isShown(panelIndex: 0))
        XCTAssertFalse(AutonomyBoundaryCaption.isShown(panelIndex: 4))
    }

    /// The committed mutation: captioning every panel. It passes "a caption is
    /// drawn" and fails the thing that matters, which is that there is one.
    func testProductionTellsOneCaptionFromFive() {
        let captionEverywhere: (Int) -> Bool = { _ in true }
        XCTAssertEqual((0..<5).filter(captionEverywhere).count, 5)
        XCTAssertEqual((0..<5).filter { AutonomyBoundaryCaption.isShown(panelIndex: $0) }.count, 1)
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

    /// ONE HUE, TWO WEIGHTS: the bars are the line's hue, quieter. What went is
    /// a colour per percentile — three equally loud curves and a legend that
    /// had to be decoded before the chart said anything.
    @MainActor
    func testBarsAreTheLineHue() {
        for appearance in [NSAppearance(named: .aqua)!, NSAppearance(named: .darkAqua)!] {
            let line = rgb(AutonomyPalette.lineColor, appearance)
            let bars = rgb(AutonomyPalette.bars, appearance)
            XCTAssertEqual(line.r, bars.r, accuracy: 0.02, "\(appearance.name.rawValue): red")
            XCTAssertEqual(line.g, bars.g, accuracy: 0.02, "\(appearance.name.rawValue): green")
            XCTAssertEqual(line.b, bars.b, accuracy: 0.02, "\(appearance.name.rawValue): blue")
            XCTAssertLessThan(bars.a, line.a,
                              "\(appearance.name.rawValue): a histogram at the line's full strength " +
                              "competes with the line above it")
        }
    }

    func testTheKeyHasTwoEntriesOnePerMark() {
        let entries = AutonomyPalette.keyEntries
        XCTAssertEqual(entries.map(\.kind), [.line, .bars])
        XCTAssertEqual(entries.map(\.id), ["line", "bars"])
    }

    /// The p50/band key went with the percentiles: a key naming something the
    /// panels do not draw would promise a distinction that no longer exists.
    func testTheKeyExplainsItselfInWordsAndNamesNoPercentile() {
        let labels = AutonomyPalette.keyEntries.map(\.label).joined(separator: " ")
        XCTAssertTrue(labels.contains("longest run"), "got: \(labels)")
        XCTAssertTrue(labels.contains("at once"), "got: \(labels)")
        for gone in ["p50", "p95", "spread", "typical"] {
            XCTAssertFalse(labels.contains(gone), "\(gone) survives in the key: \(labels)")
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
