import SwiftUI
import XCTest

@testable import Irrlicht

/// The Autonomy section's AGGREGATE element (chart=autonomy_duration), restored
/// in #1905 after #1919 deleted it.
///
/// Everything here except `testTheAggregateAxisIsLinearAndStartsAtZero` is a
/// LOCK: it pins behaviour that existed before, so it passes by construction
/// against the restored code and cannot have been seen red first. What it CAN
/// show is that the restore is faithful — the gap rule, the thin-bucket split
/// and the shared seam all behave as they did.
final class HistoryAutonomyDurationTests: XCTestCase {

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
        XCTAssertEqual(r.thinCount, 1)
        XCTAssertEqual(r.sampleFloor, 20)
        XCTAssertEqual(r.earliestSpan, 1_700_000_000)
        XCTAssertEqual(r.totalRecorded, 33)
        XCTAssertTrue(r.hasData)
        XCTAssertEqual(r.summary.p95, 100)
        XCTAssertEqual(r.summary.count, 33)
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
        XCTAssertEqual(r.thinCount, 0)
    }

    /// Provenance, kinds and measurement are optional so a payload from a daemon
    /// that predates them decodes rather than failing outright — and absent
    /// means SAY NOTHING, which is not the same claim as "there were none".
    func testAPayloadWithoutTheCensusesStillDecodes() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"buckets":[{"ts":1,"p95":9,"p50":5,"p5":1,"min":1,"max":9,"count":2}],
         "summary":{"p95":9,"p50":5,"p5":1,"min":1,"max":9,"count":2},
         "sample_floor":20,"earliest_span":0,"total_recorded":2}
        """
        let r = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertNil(r.kinds)
        XCTAssertEqual(r.provenanceOrNone, .none)
        XCTAssertEqual(r.measurementOrNone, .none)
    }

    // MARK: The LINEAR axis (#1905)

    /// The maintainer's explicit call. The committed mutation is `logLower` —
    /// the log domain's floor of 1s, which exists only because a log scale
    /// cannot plot 0.
    func testTheAggregateAxisIsLinearAndStartsAtZero() throws {
        let d = try duration(starts: [1, 2], present: [1, 2])
        XCTAssertEqual(d.yDomain.lowerBound, 0,
                       "a linear axis whose origin is not zero exaggerates every difference above it")
        // The drawn p95 is 90 and the true max 90, so the domain is 90 * 1.1.
        XCTAssertEqual(d.yDomain.upperBound, 99, accuracy: 0.001)

        let logLower = Swift.max(1.0, 10.0 * 0.8)
        XCTAssertNotEqual(logLower, d.yDomain.lowerBound,
                          "the log domain's 1s floor has no reason to exist on a linear axis")
    }

    /// THE DEFECT: the domain was `max(p95, max)`, and `max` is a value the
    /// chart does not draw. It draws p95, p50, p5 and the plane between p95 and
    /// p5; the true extremes are FIGURES in the summary row, which is the whole
    /// reason #1905 made them figures — "one four-hour run left going overnight
    /// would otherwise redraw the whole Y scale and flatten every other bucket
    /// into the floor".
    ///
    /// Measured on the reference machine's live span log (30-day window, 27 of
    /// 30 buckets with data, 2735 runs): highest p95 1h37m against a highest max
    /// of 11h39m — a 7.19x inflation that put the tallest p50 at 0.92% of the
    /// plot height and the median p50 at 0.46%, i.e. on the axis, with 87.4% of
    /// the plot empty above the band.
    ///
    /// The committed mutation is `scaledToMax` below: the domain that shipped.
    func testTheDomainFitsWhatIsDrawnNotAnUndrawnOutlier() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1,2],
         "buckets":[{"ts":1,"p95":5820,"p50":424,"p5":60,"min":60,"max":41940,"count":300},
                    {"ts":2,"p95":3000,"p50":210,"p5":30,"min":30,"max":4000,"count":120}],
         "summary":{"p95":5820,"p50":424,"p5":60,"min":30,"max":41940,"count":420},
         "sample_floor":20,"earliest_span":0,"total_recorded":420}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))

        // The top tracks the highest DRAWN p95.
        XCTAssertEqual(d.yDomain.upperBound, 5820 * 1.1, accuracy: 0.001)

        // …and the p50 line stays legible rather than collapsing onto the axis.
        let tallestP50 = d.buckets.map(\.p50).max() ?? 0
        XCTAssertGreaterThan(tallestP50 / d.yDomain.upperBound, 0.05)

        // THE MUTATION: the shipped domain, scaled to the undrawn max.
        let scaledToMax = (d.buckets.flatMap { [$0.p95, $0.max] }.max() ?? 0) * 1.1
        XCTAssertGreaterThan(scaledToMax, d.yDomain.upperBound * 5,
                             "the mutation is indistinguishable from production on this fixture")
        XCTAssertLessThan(tallestP50 / scaledToMax, 0.01)
    }

    /// Dropping `max` from the AXIS must not drop it from the section: the
    /// figure row under the chart still states the true extremes, which is
    /// where the ticket wanted them.
    func testTheExtremesKeepTheirPlaceAsFigures() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],
         "buckets":[{"ts":1,"p95":5820,"p50":424,"p5":60,"min":60,"max":41940,"count":300}],
         "summary":{"p95":5820,"p50":424,"p5":60,"min":6,"max":41940,"count":420},
         "sample_floor":20,"earliest_span":0,"total_recorded":420}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.summary.max, 41_940, "the outlier is still reported, as a number")
        XCTAssertEqual(d.summary.min, 6)
        XCTAssertEqual(AutonomyFormat.duration(d.summary.max), "11h39m")
        XCTAssertGreaterThan(d.summary.max, d.yDomain.upperBound,
                             "a figure above the top of its own chart is exactly the point: it is a "
                             + "number, not a line")
    }

    /// What linear COSTS, recorded rather than hidden: one day whose p95 really
    /// was 11h39m sets the domain, and a typical day's 10m p50 then sits in the
    /// bottom ~1.5% of the plot. The figure row under the chart carries
    /// p95/p50/p5 and the extremes as NUMBERS for exactly this reason.
    ///
    /// The long value here is a p95, not a `max`, because the axis reads only
    /// what it draws — see `testTheDomainFitsWhatIsDrawnNotAnUndrawnOutlier`.
    /// The cost is real either way; what changed is that it is now paid to a
    /// line the reader can see.
    func testTheCostOfALinearAxisIsRecorded() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1,2],
         "buckets":[{"ts":1,"p95":41940,"p50":9000,"p5":600,"min":600,"max":43000,"count":30},
                    {"ts":2,"p95":1200,"p50":600,"p5":60,"min":60,"max":1500,"count":30}],
         "summary":{"p95":41940,"p50":600,"p5":60,"min":60,"max":43000,"count":60},
         "sample_floor":20,"earliest_span":0,"total_recorded":60}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.yDomain.upperBound, 41_940 * 1.1, accuracy: 0.001)
        XCTAssertLessThan(600 / d.yDomain.upperBound, 0.02,
                          "the quiet day's typical run is flattened toward the floor — the cost of a "
                          + "linear axis, paid to a p95 the chart actually draws")
    }

    func testAnEmptyWindowStillHasAPlottableDomain() throws {
        let d = try duration(starts: [1, 2], present: [])
        XCTAssertGreaterThan(d.yDomain.upperBound, d.yDomain.lowerBound,
                             "a zero-width domain would make Swift Charts draw nothing at all")
    }

    // MARK: The band's geometry

    /// The daemon OMITS empty buckets, and reading `buckets` directly hands
    /// Swift Charts a dense list in which the gap simply is not there — so it
    /// connects straight across. `alignedBuckets` is what puts the hole back.
    func testAlignedBucketsPutTheGapBack() throws {
        let d = try duration(starts: [100, 200, 300, 400], present: [100, 400])
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
        let d = try duration(starts: [100, 200, 300, 400, 500], present: [100, 200, 400, 500])
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

    /// THE COMMITTED MUTATION: the "one polygon over everything present" build —
    /// the shape a fill takes by default, and the shape that silently claims the
    /// gap. It answers identically for a gapped range and an unbroken one;
    /// production must not.
    func testProductionTellsAGappedRangeFromAnUnbrokenOne() throws {
        let gapped = try duration(starts: [1, 2, 3], present: [1, 3]).alignedBuckets
        let whole = try duration(starts: [1, 2, 3], present: [1, 2, 3]).alignedBuckets

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

    /// A thin bucket's p95 IS its maximum and its p5 IS its minimum. Inside one
    /// smooth plane that distinction disappears, so the plane splits there and
    /// the thin stretch is filled from its own fainter token.
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
        // Adjacent segments SHARE their boundary index — a seam would show as a
        // hairline crack down the band.
        for i in 1..<segments.count {
            XCTAssertEqual(segments[i].from, segments[i - 1].to)
        }
    }

    /// The stroke dashes a segment either of whose ends is thin; the fill has to
    /// split on the same rule, or the dashes and the plane disagree about where
    /// the thin stretch is.
    func testThinnessBelongsToTheIntervalNotTheBucket() {
        let segments = AutonomyBandLayout.segments(points: [bucket(1), bucket(2, thin: true), bucket(3)])
        XCTAssertEqual(segments, [AutonomyBandLayout.Segment(from: 0, to: 2, thin: true)])
    }

    /// A lone bucket has no neighbour to make an area with. Reported as a
    /// zero-width segment so the chart can draw its spread as a whisker rather
    /// than leaving the one bucket that most needs a range with none.
    func testAnIsolatedBucketIsAZeroWidthSegment() throws {
        let points = try duration(starts: [1, 2, 3], present: [2]).alignedBuckets
        let segments = AutonomyBandLayout.segments(points: points)
        XCTAssertEqual(segments, [AutonomyBandLayout.Segment(from: 1, to: 1, thin: false)])
        XCTAssertTrue(segments[0].isIsolated)
    }

    func testAnEmptyAxisProducesNoSegments() {
        XCTAssertTrue(AutonomyBandLayout.segments(points: []).isEmpty)
        XCTAssertTrue(AutonomyBandLayout.segments(points: [nil, nil]).isEmpty)
    }

    // MARK: The boundary rule lands on the same instant as the panel's

    /// The two elements share one window, so a boundary is at the same fraction
    /// of each — which is what lets one caption on the top element explain a
    /// rule drawn on both.
    func testBoundaryFractionMatchesThePanelsForTheSameWindow() throws {
        let starts: [Int64] = [0, 100, 200, 300, 400]
        let boundary = HistoryAutonomyBoundary(ts: 200, from: "cost", to: "log")
        let d = try duration(starts: starts, present: [0, 400],
                             boundaries: [["ts": 200, "from": "cost", "to": "log"]])
        XCTAssertEqual(d.visibleBoundaries.map(\.ts), [200])
        XCTAssertEqual(d.domainFraction(of: boundary), 0.5, accuracy: 0.0001)
    }

    /// STRICTLY inside: a rule on the first or last bucket marks nothing and
    /// reads as a chart border.
    func testABoundaryOnTheAxisItselfIsNotDrawn() throws {
        let d = try duration(starts: [0, 100, 200], present: [0],
                             boundaries: [["ts": 0, "from": "cost", "to": "log"],
                                          ["ts": 200, "from": "log", "to": "live"]])
        XCTAssertTrue(d.visibleBoundaries.isEmpty)
    }

    // MARK: Fixtures

    private func bucket(_ ts: Int64, thin: Bool = false) -> HistoryAutonomyBucket {
        HistoryAutonomyBucket(ts: ts, p95: 90, p50: 50, p5: 10,
                              min: 10, max: 90, count: thin ? 3 : 30, thin: thin)
    }

    /// A decoded response over `starts`, carrying a bucket for each ts in
    /// `present`. Built through the DECODER rather than a memberwise init, so a
    /// wire-shape change fails here too rather than only in the decode tests.
    private func duration(starts: [Int64],
                          present: [Int64],
                          boundaries: [[String: Any]] = []) throws -> HistoryAutonomyDurationResponse {
        let buckets = present.map { ts in
            "{\"ts\":\(ts),\"p95\":90,\"p50\":50,\"p5\":10,\"min\":10,\"max\":90,\"count\":30}"
        }.joined(separator: ",")
        let rules = boundaries.map { b in
            "{\"ts\":\(b["ts"] ?? 0),\"from\":\"\(b["from"] ?? "")\",\"to\":\"\(b["to"] ?? "")\"}"
        }.joined(separator: ",")
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":\(starts.first ?? 0),
         "end":\(starts.last ?? 0),"bucket_seconds":86400,
         "bucket_starts":[\(starts.map(String.init).joined(separator: ","))],
         "buckets":[\(buckets)],
         "summary":{"p95":90,"p50":50,"p5":10,"min":10,"max":90,"count":\(present.count * 30)},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":\(present.count * 30),
         "provenance":{"reconstructed":0,"cost_derived":0,"live_since":0,"boundaries":[\(rules)]}}
        """
        return try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
    }
}
