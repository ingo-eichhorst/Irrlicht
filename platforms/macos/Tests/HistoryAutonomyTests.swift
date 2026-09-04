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
