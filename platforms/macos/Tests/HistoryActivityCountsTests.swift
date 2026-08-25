import XCTest
@testable import Irrlicht

/// Unit coverage for `HistoryStateResponse.Counts` and the Activity Matrix's
/// CSV export — separate from the snapshot tests of the rendered chart.
/// Regression guard for #1821: the macOS Activity Matrix hard-coded a
/// three-field `Counts` (working/waiting/ready) and so dropped the `error`
/// series the daemon has sent since #1801 — which also meant the other three
/// segments were normalized against a `total` that silently excluded errors,
/// drawing them proportionally too large in exactly the buckets that have
/// something to report. Mirrors the fixture web's `irrlicht.test.js` added
/// for the identical bug (#1801, `stateCellCounts carries the error bucket`).
final class HistoryActivityCountsTests: XCTestCase {
    /// A single project/3-bucket fixture whose last bucket has data in all
    /// four canonical states, including a non-zero `error` count.
    private func fixtureWithError() -> HistoryStateResponse {
        let hour: Int64 = 3_600
        let base: Int64 = 1_700_000_000
        let buckets = (0..<3).map { base + Int64($0) * hour }
        return HistoryStateResponse(
            range: "24h", chart: "state", group: "project",
            start: base, end: base + 3 * hour,
            bucketSeconds: hour, bucketStarts: buckets,
            projects: ["irrlicht"],
            byState: [
                "working": ["irrlicht": [1, 0, 0]],
                "waiting": ["irrlicht": [0, 1, 1]],
                "ready":   ["irrlicht": [0, 0, 1]],
                "error":   ["irrlicht": [0, 0, 2]],
            ],
            concurrency: nil,
            scope: nil
        )
    }

    /// `Counts.total` must sum every canonical state the daemon can send, not
    /// only the three the struct historically hard-coded. Deliberately reads
    /// the expected value straight from `byState` (not through `Counts`,
    /// which is the thing under test) so this compiles and is meaningful
    /// whether or not `Counts` has an `error` field yet: on `main` today
    /// bucket 2 sums 0 working + 1 waiting + 1 ready + 2 error = 4, but
    /// `Counts.total` only adds the first three and reports 2.
    func testTotalSumsEveryStateInByState() {
        let data = fixtureWithError()
        for bucketIndex in 0..<3 {
            let expected = ["working", "waiting", "ready", "error"].reduce(0.0) { sum, state in
                sum + (data.byState[state]?["irrlicht"]?[bucketIndex] ?? 0)
            }
            let counts = data.counts(project: "irrlicht", bucketIndex: bucketIndex)
            XCTAssertEqual(counts.total, expected, "bucket \(bucketIndex) total should sum all four states")
        }
    }

    /// The CSV export must carry an `error` column, matching the web
    /// `stateCsvLines`'s `bucket_start,project,working,waiting,ready,error`
    /// header and per-row error count.
    func testCSVExportIncludesErrorColumn() {
        let data = fixtureWithError()
        let csv = HistoryExport.csvState(data)
        let lines = csv.split(separator: "\n").map(String.init)
        XCTAssertEqual(lines.first, "bucket_start,project,working,waiting,ready,error")
        // lines[0] is the header; lines[1...3] are the three buckets in order,
        // so lines[3] is bucket index 2, whose error count is 2.
        XCTAssertEqual(lines.count, 4)
        XCTAssertTrue(lines[3].hasSuffix(",2"), "expected a trailing error count of 2 in row: \(lines[3])")
    }
}
