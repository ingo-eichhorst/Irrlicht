import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Autonomy section (#1905): the decode contract, the strip's pixel-collapse
/// rule, and the two ladders that must not drift apart.
final class HistoryAutonomyTests: XCTestCase {

    // MARK: The collapse ladder

    /// The strip's ladder and the session-history bar's ladder are two
    /// hand-written copies of one ORDER — not of one numbering: the bar also
    /// ranks `working`, which is never a span's end reason, so the absolute
    /// values differ by construction and only the order can be compared. This
    /// is what stops the two drifting, the same job
    /// `TestAutonomyReasonLadderMatchesHistoryBar` does on the daemon side.
    @MainActor
    func testCollapseLadderMatchesTheHistoryBar() {
        let manager = SessionManager()
        let ordered = AutonomyEndReason.allCases.sorted { $0.priority > $1.priority }
        for i in 1..<ordered.count {
            let higher = ordered[i - 1]
            let lower = ordered[i]
            XCTAssertGreaterThan(
                manager.historyPriorityForState(higher.rawValue),
                manager.historyPriorityForState(lower.rawValue),
                "the autonomy strip ranks \(higher.rawValue) above \(lower.rawValue), but the " +
                "session-history bar does not — the two ladders have drifted")
        }
        // Asserted one rung per line: a single line naming three of the four
        // canonical states is what tools/state-vocabulary-lint.sh refuses.
        XCTAssertEqual(ordered[0].rawValue, "error")
        XCTAssertEqual(ordered[1].rawValue, "waiting")
        XCTAssertEqual(ordered[2].rawValue, "ready")
    }

    // MARK: The pixel-collapse rule

    private func row(_ start: Int64, _ end: Int64, _ reason: String, project: String = "p") -> HistoryAutonomySpanRow {
        decodeRow("""
        {"start":\(start),"end":\(end),"project":"\(project)","session":"s\(start)","reason":"\(reason)"}
        """)
    }

    /// Decoding a literal fixture cannot fail; a malformed one is a bug in the
    /// test, so it reports as a test failure with the offending JSON rather
    /// than as a crash (`try!`) with no context.
    private func decodeRow(_ json: String, file: StaticString = #filePath, line: UInt = #line) -> HistoryAutonomySpanRow {
        do {
            return try JSONDecoder().decode(HistoryAutonomySpanRow.self, from: Data(json.utf8))
        } catch {
            XCTFail("fixture is not a decodable span row: \(error)\n\(json)", file: file, line: line)
            return HistoryAutonomySpanRow(start: 0, end: 0, project: "", session: "", reason: nil)
        }
    }

    /// Every span draws at a minimum of one column. At 12 months a 40-second
    /// run is far under one pixel; rounding it away would erase exactly the
    /// short runs the p5 line is about.
    func testSubPixelSpanStillDrawsOneColumn() {
        // A 1-second run inside a 100,000-second window drawn 10 columns wide:
        // its true width is 0.0001 columns.
        let cells = AutonomyStripLayout.collapse(
            spans: [row(50_000, 50_001, "ready")],
            start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(cells.count, 10)
        let occupied = cells.filter(\.occupied)
        XCTAssertEqual(occupied.count, 1, "a sub-pixel run must still occupy exactly one column, never zero")
        XCTAssertEqual(occupied.first?.reason, .ready)
    }

    /// When one column holds several spans, the column takes the
    /// highest-priority end reason — error over waiting over ready.
    func testSharedColumnTakesTheHighestPriorityReason() {
        // Three runs inside the same 1/10th of the window.
        let spans = [
            row(1_000, 1_100, "ready"),
            row(1_200, 1_300, "error"),
            row(1_400, 1_500, "waiting"),
        ]
        let cells = AutonomyStripLayout.collapse(spans: spans, start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(cells[0].reason, .error,
                       "a column holding several runs must paint the highest-priority reason — one error " +
                       "in a column paints the whole column")

        // Order must not matter: the ladder decides, not arrival.
        let reversed = AutonomyStripLayout.collapse(spans: spans.reversed(), start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(reversed[0].reason, .error)

        // And without the error, waiting beats ready.
        let noError = AutonomyStripLayout.collapse(
            spans: [row(1_000, 1_100, "ready"), row(1_400, 1_500, "waiting")],
            start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(noError[0].reason, .waiting)
    }

    /// A long run spans every column it covers, so the strip still reads as a
    /// length and not just a marker.
    func testLongSpanCoversItsColumns() {
        let cells = AutonomyStripLayout.collapse(
            spans: [row(0, 50_000, "waiting")],
            start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(cells.filter(\.occupied).count, 6,
                       "half the window plus the column its end falls in")
    }

    func testSpansOutsideTheWindowAreClippedNotDrawn() {
        let before = AutonomyStripLayout.collapse(
            spans: [row(-5_000, -1_000, "ready")], start: 0, end: 100_000, columns: 10)
        XCTAssertTrue(before.allSatisfy { !$0.occupied })

        let straddling = AutonomyStripLayout.collapse(
            spans: [row(-5_000, 10_000, "ready")], start: 0, end: 100_000, columns: 10)
        XCTAssertTrue(straddling[0].occupied, "the visible part of a straddling run must still draw")
        XCTAssertFalse(straddling[5].occupied)
    }

    /// A run whose reason this build cannot name still HAPPENED: it occupies
    /// its column (drawn neutral) rather than reading as idle.
    func testUnnamedReasonStillOccupiesItsColumn() {
        let unknown = decodeRow(#"{"start":10,"end":20,"project":"p","session":"s","reason":"martian"}"#)
        let cells = AutonomyStripLayout.collapse(spans: [unknown], start: 0, end: 100, columns: 10)
        XCTAssertTrue(cells[1].occupied, "an unrecognized reason must not erase the run")
        XCTAssertNil(cells[1].reason)

        // …and it must never outrank a real reason in a shared column.
        let mixed = AutonomyStripLayout.collapse(
            spans: [unknown, row(11, 19, "error")], start: 0, end: 100, columns: 10)
        XCTAssertEqual(mixed[1].reason, .error)
    }

    func testDegenerateInputsProduceNoColumns() {
        XCTAssertTrue(AutonomyStripLayout.collapse(spans: [], start: 0, end: 0, columns: 10).isEmpty)
        XCTAssertTrue(AutonomyStripLayout.collapse(spans: [], start: 0, end: 100, columns: 0).isEmpty)
    }

    // MARK: Decoding

    func testDurationResponseDecodesIncludingOmittedThin() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1,2],
         "buckets":[{"ts":1,"p95":100,"p50":50,"p5":10,"min":10,"max":100,"count":30},
                    {"ts":2,"p95":9,"p50":5,"p5":1,"min":1,"max":9,"count":3,"thin":true}],
         "summary":{"p95":100,"p50":40,"p5":5,"min":1,"max":100,"count":33},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":33}
        """
        let r = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.buckets.count, 2)
        XCTAssertFalse(r.buckets[0].thin, "`thin` is omitempty on the wire — absent must decode as false")
        XCTAssertTrue(r.buckets[1].thin)
        XCTAssertEqual(r.sampleFloor, 20)
        XCTAssertEqual(r.earliestSpan, 1_700_000_000)
        XCTAssertEqual(r.totalRecorded, 33)
        XCTAssertTrue(r.hasData)
    }

    func testEmptyBucketListIsNoData() throws {
        let json = """
        {"window":"1y","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":604800,
         "bucket_starts":[1,2,3],"buckets":[],
         "summary":{"p95":0,"p50":0,"p5":0,"min":0,"max":0,"count":0},
         "sample_floor":20,"earliest_span":0,"total_recorded":0}
        """
        let r = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertFalse(r.hasData, "a fully-drawn axis with no buckets is still \"no data\"")
        XCTAssertFalse(r.bucketStarts.isEmpty, "the axis is still sent, so the chart can say what window it is")
    }

    func testSpansResponseDecodesAndGroups() throws {
        let json = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"s1","reason":"ready"},
                  {"start":2,"end":8,"project":"b","session":"s2","reason":"error"}],
         "projects":["b","a"],"earliest_span":1,"total_recorded":2,"truncated":false}
        """
        let r = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.projects, ["b", "a"], "row order is the daemon's, not re-derived here")
        XCTAssertEqual(r.spans(for: "a").count, 1)
        XCTAssertEqual(r.spans(for: "a").first?.endReason, .ready)
        XCTAssertEqual(r.spans(for: "a").first?.duration, 8)
    }

    // MARK: The two window vocabularies stay distinct

    /// The macOS twin of the daemon's window-vocabulary tripwire: the Autonomy
    /// pickers send window LENGTHS, while `HistoryGranularity`'s same-looking
    /// keys are bucket widths. Nothing may quietly reuse one for the other.
    func testAutonomyWindowsAreNotGranularities() {
        let autonomyKeys = Set(HistoryAutonomySpanWindow.allCases.map(\.rawValue))
        XCTAssertTrue(autonomyKeys.contains("12mo"),
                      "12mo is the strip's own key and has no granularity twin — if it is gone, the two " +
                      "vocabularies may have been merged")
        XCTAssertFalse(Set(HistoryGranularity.allCases.map(\.rawValue)).contains("12mo"))
        // The overlapping keys must not be sourced from the granularity enum.
        for key in ["8h", "24h", "7d"] {
            XCTAssertNotNil(HistoryAutonomySpanWindow(rawValue: key))
            XCTAssertNotNil(HistoryGranularity(rawValue: key),
                            "the overlap this check guards is gone; re-check whether it still covers the trap")
        }
        XCTAssertEqual(Set(HistoryAutonomyRange.allCases.map(\.rawValue)), ["30d", "1y"])
    }

    // MARK: The chart's key, and the strip's bounds

    /// The chart's colour scale still has to cover every drawn line: a line
    /// with no colour is a line drawn in fallback grey.
    func testSeriesColorsCoverEveryLine() {
        XCTAssertEqual(AutonomyPalette.seriesOrder, ["p95", "p50", "p5"])
        for key in AutonomyPalette.seriesOrder {
            XCTAssertNotNil(AutonomyPalette.seriesColors[key],
                            "\(key) is drawn but has no colour, so the key cannot name it")
            XCTAssertNotNil(AutonomyPalette.seriesRoles[key],
                            "\(key) is drawn but has no role, so nothing decides its weight")
        }
        XCTAssertEqual(AutonomyPalette.seriesRange.count, AutonomyPalette.seriesOrder.count)
    }

    /// The INVERSION of the old `…AndAreDistinct`. Distinct hues are what the
    /// redesign removed: p95 and p5 are the two EDGES of one range, and a hue
    /// each made them read as two independent measurements. The web twin is
    /// `the three lines share one colour, and roles carry the difference`.
    func testTheThreeLinesShareOneColourAndRolesCarryTheDifference() {
        let colours = Set(AutonomyPalette.seriesRange.map { String(describing: $0) })
        XCTAssertEqual(colours.count, 1, "the three lines must be one hue — weight is what separates them")
        XCTAssertEqual(AutonomyPalette.seriesOrder.map { AutonomyPalette.role(of: $0) },
                       [.edge, .line, .edge])
        XCTAssertEqual(AutonomyPalette.seriesRoles.values.filter { $0 == .line }.count, 1,
                       "more than one headline line makes \"the typical run\" ambiguous")
        // The line carries the weight; the edges stay out of its way.
        XCTAssertGreaterThan(AutonomyPalette.lineWidth(.line), AutonomyPalette.lineWidth(.edge))
        XCTAssertGreaterThan(AutonomyPalette.opacity(.line, thin: false),
                             AutonomyPalette.opacity(.edge, thin: false))
        // …and a thin bucket is quieter than a measured one on both roles.
        XCTAssertLessThan(AutonomyPalette.opacity(.line, thin: true),
                          AutonomyPalette.opacity(.line, thin: false))
        XCTAssertLessThan(AutonomyPalette.opacity(.edge, thin: true),
                          AutonomyPalette.opacity(.edge, thin: false))
    }

    /// The key is two entries, because the chart draws two things — a line and
    /// a plane. Three percentile swatches in one hue would promise a
    /// distinction the chart stopped making, which is why the Swift Charts
    /// legend derived from `seriesOrder` is hidden and this is drawn instead.
    func testTheKeyHasTwoEntriesBothFromTheOneTable() {
        let entries = AutonomyPalette.keyEntries
        XCTAssertEqual(entries.count, 2)
        XCTAssertEqual(entries.map(\.kind), [.line, .band])
        for entry in entries {
            // `from` names a row of the one table, not a label someone invented.
            XCTAssertTrue(AutonomyPalette.seriesOrder.contains(entry.from),
                          "\(entry.from) is not a line the chart draws")
        }
        XCTAssertEqual(String(describing: entries[0].color),
                       String(describing: AutonomyPalette.seriesColors["p50"]!),
                       "the line swatch must be the very colour the p50 line is stroked in")
        XCTAssertEqual(String(describing: entries[1].fill), String(describing: Optional(AutonomyPalette.band)))
        XCTAssertEqual(String(describing: entries[1].color), String(describing: AutonomyPalette.edge))
        XCTAssertNil(entries[0].fill, "a line has no area; a fill would claim one")
    }

    /// Both labels name their percentiles AND say what they mean in words:
    /// "p5–p95" is not something a reader who has never seen a percentile can
    /// read. Same two strings as the web panel's key.
    func testTheKeyExplainsItselfInWords() {
        let entries = AutonomyPalette.keyEntries
        XCTAssertEqual(entries[0].label, "p50 · the typical run")
        XCTAssertEqual(entries[1].label, "p5–p95 · the usual spread")
    }

    /// The band is a WEIGHT of the line's hue, not a fourth colour. Were it an
    /// independent value, "one hue" would hold for the three strokes and
    /// quietly fail for the largest area of ink on the chart. Checked in BOTH
    /// appearances, because the band's alpha differs per appearance and a
    /// hand-typed hex is exactly where a wrong triple would hide.
    @MainActor
    func testBandIsTheLineHueInBothAppearances() {
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            let line = srgb(AutonomyPalette.lineColor, in: appearance)
            for (name, colour) in [("band", AutonomyPalette.band),
                                   ("bandThin", AutonomyPalette.bandThin),
                                   ("edge", AutonomyPalette.edge)] {
                let got = srgb(colour, in: appearance)
                XCTAssertEqual(got.r, line.r, accuracy: 0.004, "\(name) is a different hue in \(appearance.rawValue)")
                XCTAssertEqual(got.g, line.g, accuracy: 0.004, "\(name) is a different hue in \(appearance.rawValue)")
                XCTAssertEqual(got.b, line.b, accuracy: 0.004, "\(name) is a different hue in \(appearance.rawValue)")
            }
        }
    }

    /// …and the band is genuinely translucent, in both appearances and with a
    /// fainter plane for thin buckets. An opaque plane would bury the line it
    /// is supposed to sit behind.
    @MainActor
    func testBandIsTranslucentAndThinIsFainter() {
        for appearance in [NSAppearance.Name.aqua, .darkAqua] {
            let band = alpha(AutonomyPalette.band, in: appearance)
            let thin = alpha(AutonomyPalette.bandThin, in: appearance)
            let edge = alpha(AutonomyPalette.edge, in: appearance)
            XCTAssertGreaterThan(band, 0, "an invisible band draws nothing at all")
            XCTAssertLessThan(band, 0.5, "a plane this solid competes with the line inside it")
            XCTAssertLessThan(thin, band, "a thin bucket's plane must be fainter than a measured one's")
            XCTAssertGreaterThan(edge, band, "the edges must read as the band's boundary")
            XCTAssertLessThan(edge, 1, "a full-strength edge is a fourth curve, not a boundary")
        }
    }

    /// Resolved sRGB of a possibly-appearance-dependent colour. Same technique
    /// `TokenContrastTests.srgb` uses; force-unwraps on purpose, since a colour
    /// this cannot resolve must fail the test rather than compare as equal.
    @MainActor
    private func srgb(_ color: Color, in appearance: NSAppearance.Name) -> (r: Double, g: Double, b: Double) {
        var out = (r: 0.0, g: 0.0, b: 0.0)
        NSAppearance(named: appearance)!.performAsCurrentDrawingAppearance {
            let ns = NSColor(color).usingColorSpace(.sRGB)!
            out = (Double(ns.redComponent), Double(ns.greenComponent), Double(ns.blueComponent))
        }
        return out
    }

    @MainActor
    private func alpha(_ color: Color, in appearance: NSAppearance.Name) -> Double {
        var out = 0.0
        NSAppearance(named: appearance)!.performAsCurrentDrawingAppearance {
            out = Double(NSColor(color).usingColorSpace(.sRGB)!.alphaComponent)
        }
        return out
    }

    // MARK: The band's geometry

    private func bucket(_ ts: Int64, thin: Bool = false) -> HistoryAutonomyBucket {
        decodeBucket("""
        {"ts":\(ts),"p95":90,"p50":50,"p5":10,"min":10,"max":90,"count":\(thin ? 3 : 30),"thin":\(thin)}
        """)
    }

    private func decodeBucket(_ json: String, file: StaticString = #filePath, line: UInt = #line) -> HistoryAutonomyBucket {
        do {
            return try JSONDecoder().decode(HistoryAutonomyBucket.self, from: Data(json.utf8))
        } catch {
            XCTFail("fixture is not a decodable bucket: \(error)\n\(json)", file: file, line: line)
            return decodeBucket("""
            {"ts":0,"p95":1,"p50":1,"p5":1,"min":1,"max":1,"count":1}
            """)
        }
    }

    /// The daemon OMITS empty buckets, and reading `buckets` directly hands
    /// Swift Charts a dense list in which the gap simply is not there — so it
    /// connects straight across. `alignedBuckets` is what puts the hole back.
    func testAlignedBucketsPutTheGapBack() throws {
        let d = try durationWithBuckets(starts: [100, 200, 300, 400], present: [100, 400])
        let aligned = d.alignedBuckets
        XCTAssertEqual(aligned.count, 4)
        XCTAssertNotNil(aligned[0])
        XCTAssertNil(aligned[1], "an omitted bucket is a GAP, not a zero")
        XCTAssertNil(aligned[2])
        XCTAssertNotNil(aligned[3])
    }

    /// A LINE drawn across a gap interpolates; a FILLED AREA drawn across one
    /// paints a whole plane over days that hold no runs at all.
    func testABandSegmentNeverSpansAnEmptyBucket() throws {
        let d = try durationWithBuckets(starts: [100, 200, 300, 400, 500], present: [100, 200, 400, 500])
        let points = d.alignedBuckets
        let segments = AutonomyBandLayout.segments(points: points)
        XCTAssertEqual(segments.map { [$0.from, $0.to] }, [[0, 1], [3, 4]])
        // Stated as the property, not just the shape.
        for segment in segments {
            for i in segment.from...segment.to {
                XCTAssertNotNil(points[i], "segment \(segment.id) covers an omitted bucket")
            }
        }
    }

    /// THE COMMITTED MUTATION: the "one polygon over everything present"
    /// build — the shape a fill takes by default, and the shape that silently
    /// claims the gap. It answers identically for a gapped range and an
    /// unbroken one; production must not.
    func testProductionTellsAGappedRangeFromAnUnbrokenOne() throws {
        let gapped = try durationWithBuckets(starts: [1, 2, 3], present: [1, 3]).alignedBuckets
        let whole = try durationWithBuckets(starts: [1, 2, 3], present: [1, 2, 3]).alignedBuckets

        let bridging: ([HistoryAutonomyBucket?]) -> [AutonomyBandLayout.Segment] = {
            [AutonomyBandLayout.Segment(from: 0, to: $0.count - 1, thin: false)]
        }
        XCTAssertEqual(bridging(gapped), bridging(whole))

        XCTAssertEqual(AutonomyBandLayout.segments(points: whole),
                       [AutonomyBandLayout.Segment(from: 0, to: 2, thin: false)])
        XCTAssertNotEqual(AutonomyBandLayout.segments(points: gapped),
                          AutonomyBandLayout.segments(points: whole))
        // Two isolated buckets — a bridging build would have drawn one plane
        // straight over the empty day between them.
        XCTAssertEqual(AutonomyBandLayout.segments(points: gapped),
                       [AutonomyBandLayout.Segment(from: 0, to: 0, thin: false),
                        AutonomyBandLayout.Segment(from: 2, to: 2, thin: false)])
    }

    /// A thin bucket's p95 IS its maximum and its p5 IS its minimum. Inside
    /// one smooth plane that distinction disappears, so the plane splits there
    /// and the thin stretch is filled from its own fainter token.
    func testAThinStretchIsItsOwnSegmentAndTheSeamIsShared() {
        let points: [HistoryAutonomyBucket?] = [
            bucket(1), bucket(2), bucket(3, thin: true), bucket(4), bucket(5),
        ]
        let segments = AutonomyBandLayout.segments(points: points)
        XCTAssertEqual(segments, [
            AutonomyBandLayout.Segment(from: 0, to: 1, thin: false),
            AutonomyBandLayout.Segment(from: 1, to: 3, thin: true),
            AutonomyBandLayout.Segment(from: 3, to: 4, thin: false),
        ])
        // Adjacent segments SHARE their boundary index — a seam would show as
        // a hairline crack down the band.
        for i in 1..<segments.count {
            XCTAssertEqual(segments[i].from, segments[i - 1].to)
        }
    }

    /// The stroke dashes a segment either of whose ends is thin; the fill has
    /// to split on the same rule, or the dashes and the plane disagree about
    /// where the thin stretch is.
    func testThinnessBelongsToTheIntervalNotTheBucket() {
        let segments = AutonomyBandLayout.segments(points: [bucket(1), bucket(2, thin: true), bucket(3)])
        XCTAssertEqual(segments, [AutonomyBandLayout.Segment(from: 0, to: 2, thin: true)])
    }

    /// A lone bucket has no neighbour to make an area with. Reported as a
    /// zero-width segment so the chart can draw its spread as a whisker rather
    /// than leaving the one bucket that most needs a range with none.
    func testAnIsolatedBucketIsAZeroWidthSegment() throws {
        let points = try durationWithBuckets(starts: [1, 2, 3], present: [2]).alignedBuckets
        let segments = AutonomyBandLayout.segments(points: points)
        XCTAssertEqual(segments, [AutonomyBandLayout.Segment(from: 1, to: 1, thin: false)])
        XCTAssertTrue(segments[0].isIsolated)
    }

    func testAnEmptyAxisProducesNoSegments() {
        XCTAssertTrue(AutonomyBandLayout.segments(points: []).isEmpty)
        XCTAssertTrue(AutonomyBandLayout.segments(points: [nil, nil]).isEmpty)
    }

    // MARK: The boundary caption

    /// The caption used to hang off its rule's LEFT unconditionally. It is a
    /// fixed string most of the plot wide at 9 pt, so a rule in the left third
    /// had less room than the caption needed, SwiftUI clamped the annotation
    /// to the chart's leading edge, and the caption ended up detached from the
    /// rule it describes with its arrow pointing off-chart at nothing.
    func testTheCaptionHangsOffWhicheverSideOfTheRuleHasRoom() {
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.0), .right)
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.36), .right,
                       "the measured case: a rule at 36% has more room to its right")
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.5), .left)
        XCTAssertEqual(AutonomyBoundaryCaption.side(fraction: 0.9), .left)
    }

    /// THE COMMITTED MUTATION for it: the always-left build. It answers
    /// identically for a rule near the left edge and one near the right;
    /// production must not, or "it is placed where there is room" could pass
    /// against a build that never looked at where the rule is.
    func testProductionTellsALeftRuleFromARightOne() {
        let alwaysLeft: (Double) -> AutonomyBoundaryCaption.Side = { _ in .left }
        XCTAssertEqual(alwaysLeft(0.1), alwaysLeft(0.9))
        XCTAssertNotEqual(AutonomyBoundaryCaption.side(fraction: 0.1),
                          AutonomyBoundaryCaption.side(fraction: 0.9))
    }

    /// The fraction the side is chosen from is the rule's real position in the
    /// drawn domain — the macOS twin of the web's `fraction`.
    func testBoundaryFractionIsItsPositionInTheDrawnDomain() throws {
        let d = try durationWithBuckets(starts: [0, 100, 200, 300, 400], present: [0])
        let boundary = HistoryAutonomyBoundary(ts: 100, from: "cost", to: "log")
        XCTAssertEqual(d.domainFraction(of: boundary), 0.25, accuracy: 0.0001)
    }

    /// Decodes a duration payload whose `bucket_starts` is `starts` and whose
    /// `buckets` holds only `present` — the omission the daemon really makes,
    /// exercised through the real decode path rather than a hand-built struct.
    private func durationWithBuckets(starts: [Int64], present: [Int64]) throws -> HistoryAutonomyDurationResponse {
        let startList = starts.map(String.init).joined(separator: ",")
        let bucketList = present
            .map { "{\"ts\":\($0),\"p95\":90,\"p50\":50,\"p5\":10,\"min\":10,\"max\":90,\"count\":30}" }
            .joined(separator: ",")
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[\(startList)],"buckets":[\(bucketList)],
         "summary":{"p95":90,"p50":50,"p5":10,"min":10,"max":90,"count":\(present.count)},
         "sample_floor":20,"earliest_span":1,"total_recorded":\(present.count)}
        """
        return try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
    }

    /// The strip's bounds coarsen with the window, so an 8h strip reads as a
    /// time of day and a 12mo strip as a month.
    func testAxisBoundCoarsensWithTheWindow() {
        let utc = TimeZone(identifier: "UTC")!
        // 2026-01-02T15:30:00Z
        let d = Date(timeIntervalSince1970: 1_767_367_800)
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 8 * 3600, timeZone: utc), "15:30")
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 7 * 86400, timeZone: utc), "Jan 2")
        XCTAssertEqual(AutonomyFormat.axisBound(d, windowSeconds: 365 * 86400, timeZone: utc), "Jan 2026")
    }

    /// #1659: every date this section renders takes its zone as an input.
    func testAxisBoundHonorsTheCallersZone() {
        let d = Date(timeIntervalSince1970: 1_767_367_800)
        let utc = AutonomyFormat.axisBound(d, windowSeconds: 8 * 3600, timeZone: TimeZone(identifier: "UTC")!)
        let tokyo = AutonomyFormat.axisBound(d, windowSeconds: 8 * 3600,
                                             timeZone: TimeZone(identifier: "Asia/Tokyo")!)
        XCTAssertNotEqual(utc, tokyo, "the bound must follow the zone it is given, never the machine's")
    }

    // MARK: Formatting + provenance

    func testDurationFormatting() {
        XCTAssertEqual(AutonomyFormat.duration(41), "41s")
        XCTAssertEqual(AutonomyFormat.duration(660), "11m")
        XCTAssertEqual(AutonomyFormat.duration(7080), "1h58m")
        XCTAssertEqual(AutonomyFormat.duration(86_400), "1d")
    }

    /// "No data" must never read as "you did nothing": with nothing recorded
    /// the line says so in words, and with something recorded it names the
    /// date collection started.
    func testProvenanceLineSaysWhenCollectionStarted() {
        let utc = TimeZone(identifier: "UTC")!
        let empty = AutonomyFormat.provenance(earliest: 0, total: 0, timeZone: utc)
        XCTAssertTrue(empty.contains("began measuring"),
                      "an empty section must explain that collection starts with this update, not imply idleness")

        // 1700000000 = 2023-11-14T22:13:20Z
        let seeded = AutonomyFormat.provenance(earliest: 1_700_000_000, total: 312, timeZone: utc)
        XCTAssertTrue(seeded.contains("Nov 14, 2023"), "got: \(seeded)")
        XCTAssertTrue(seeded.contains("312 runs"), "got: \(seeded)")
    }
}
