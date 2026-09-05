import XCTest
@testable import Irrlicht

/// The back-fill's marking (#1905). `tools/autonomy-backfill` reconstructs
/// pre-feature runs from logs a Mac already had; the daemon serves them like
/// any other row, and this surface has to say so — a reconstructed figure
/// rendered as a measured one is the wrong number with nothing on screen
/// admitting it.
final class HistoryAutonomyBackfillTests: XCTestCase {

    private let utc = TimeZone(identifier: "UTC")!

    // MARK: Decoding the provenance block

    func testProvenanceDecodesFromBothPayloads() throws {
        let durationJSON = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"buckets":[{"ts":1,"p95":100,"p50":50,"p5":10,"min":10,"max":100,"count":30}],
         "summary":{"p95":100,"p50":40,"p5":5,"min":1,"max":100,"count":33},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":33,
         "provenance":{"reconstructed":9,"cost_derived":4,"live_since":1755000000}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(durationJSON.utf8))
        XCTAssertEqual(d.provenanceOrNone.reconstructed, 9)
        XCTAssertEqual(d.provenanceOrNone.costDerived, 4)
        XCTAssertEqual(d.provenanceOrNone.liveSince, 1_755_000_000)

        let spansJSON = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"s1","reason":"unknown"}],
         "projects":["a"],"earliest_span":1,"total_recorded":1,"truncated":false,
         "provenance":{"reconstructed":1,"cost_derived":1,"live_since":0}}
        """
        let s = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(spansJSON.utf8))
        XCTAssertEqual(s.provenanceOrNone.reconstructed, 1)
        XCTAssertEqual(s.provenanceOrNone.liveSince, 0)
    }

    /// A payload from a daemon that predates the field must still decode. The
    /// alternative is a client that goes blank against an older daemon, which
    /// is a worse failure than saying nothing about provenance.
    func testAnAbsentProvenanceBlockDecodesAsSayingNothing() throws {
        let json = """
        {"window":"1y","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":604800,
         "bucket_starts":[1],"buckets":[],
         "summary":{"p95":0,"p50":0,"p5":0,"min":0,"max":0,"count":0},
         "sample_floor":20,"earliest_span":0,"total_recorded":0}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.provenanceOrNone, .none)
        XCTAssertFalse(d.provenanceOrNone.isReconstructed)
        XCTAssertNil(AutonomyFormat.reconstructionNote(d.provenanceOrNone, inView: 0, timeZone: utc))
    }

    /// `unknown` is not one of the three measured reasons, so it decodes to a
    /// nil `endReason` and draws in the neutral colour both surfaces already
    /// use for a reason they cannot name.
    func testUnknownReasonDecodesAsUnnamedAndDrawsNeutral() throws {
        let json = """
        {"start":1,"end":9,"project":"a","session":"s1","reason":"unknown"}
        """
        let row = try JSONDecoder().decode(HistoryAutonomySpanRow.self, from: Data(json.utf8))
        XCTAssertNil(row.endReason, "`unknown` must not decode into a measured end reason")
        XCTAssertEqual(AutonomyPalette.color(for: row.endReason), AutonomyPalette.color(for: nil))
    }

    // MARK: The note

    func testNoNoteWhenEveryRunInViewWasMeasured() {
        let p = HistoryAutonomyProvenance(reconstructed: 0, costDerived: 0, liveSince: 1_755_000_000)
        XCTAssertNil(AutonomyFormat.reconstructionNote(p, inView: 100, timeZone: utc))
    }

    func testNoteStatesHowManyAndTheBoundaryDate() throws {
        // 1755000000 = 2025-08-12T12:00:00Z
        let p = HistoryAutonomyProvenance(reconstructed: 40, costDerived: 0, liveSince: 1_755_000_000)
        let note = try XCTUnwrap(AutonomyFormat.reconstructionNote(p, inView: 100, timeZone: utc))
        XCTAssertTrue(note.contains("40 of 100 runs in view"), "got: \(note)")
        XCTAssertTrue(note.contains("not measured as they happened"), "got: \(note)")
        XCTAssertTrue(note.contains("Everything before Aug 12, 2025 is reconstructed."), "got: \(note)")
        XCTAssertFalse(note.contains("cost log"), "no cost-derived run is in range; the sentence must be absent")
    }

    func testNoteNamesTheCostDerivedRunsAndCallsTheirReasonUnknown() throws {
        let p = HistoryAutonomyProvenance(reconstructed: 90, costDerived: 55, liveSince: 1_755_000_000)
        let note = try XCTUnwrap(AutonomyFormat.reconstructionNote(p, inView: 120, timeZone: utc))
        XCTAssertTrue(note.contains("55 of them come from the cost log"), "got: \(note)")
        XCTAssertTrue(note.contains("unknown"), "got: \(note)")
        XCTAssertTrue(note.contains("not assumed"), "got: \(note)")
    }

    /// `liveSince == 0` means "nothing has ever been measured live", which is a
    /// different claim from "measured since the epoch". Printing Jan 1 1970
    /// would be a fabricated date — exactly the failure this marking exists to
    /// prevent.
    func testNoteNeverPrintsAnEpochDate() throws {
        let p = HistoryAutonomyProvenance(reconstructed: 40, costDerived: 40, liveSince: 0)
        let note = try XCTUnwrap(AutonomyFormat.reconstructionNote(p, inView: 40, timeZone: utc))
        XCTAssertTrue(note.contains("Nothing here was measured live"), "got: \(note)")
        XCTAssertFalse(note.contains("1970"), "got: \(note)")
    }

    /// The note takes its zone as an input (#1659), never `NSTimeZone.default`.
    func testNoteHonoursTheCallersZone() throws {
        // 1755014400 = 2025-08-12T16:00:00Z — still Aug 12 in UTC, already
        // Aug 13 in Tokyo (UTC+9). A date that reads the same in both zones
        // would make this test pass against a formatter that ignored the
        // parameter entirely.
        let p = HistoryAutonomyProvenance(reconstructed: 1, costDerived: 0, liveSince: 1_755_014_400)
        let tokyo = TimeZone(identifier: "Asia/Tokyo")!
        let inUTC = try XCTUnwrap(AutonomyFormat.reconstructionNote(p, inView: 1, timeZone: utc))
        let inTokyo = try XCTUnwrap(AutonomyFormat.reconstructionNote(p, inView: 1, timeZone: tokyo))
        XCTAssertTrue(inUTC.contains("Aug 12, 2025"), "got: \(inUTC)")
        XCTAssertTrue(inTokyo.contains("Aug 13, 2025"), "got: \(inTokyo)")
    }

    /// The committed mutation for this check, in the idiom the sibling suites
    /// already use (`denseBuckets`, `mergedResolver`). Both ways of getting the
    /// conditional wrong — a build that always speaks and one that never does —
    /// answer identically for the two fixtures. Production must not, or "says
    /// it when it should" and "stays silent when it should" are two assertions
    /// that could each pass against a build that never looked at the data.
    func testProductionTellsTheTwoFixturesApart() {
        let allLive = HistoryAutonomyProvenance(reconstructed: 0, costDerived: 0, liveSince: 1_755_000_000)
        let backfilled = HistoryAutonomyProvenance(reconstructed: 5, costDerived: 2, liveSince: 1_755_000_000)

        let alwaysSpeaks: (HistoryAutonomyProvenance) -> String? = { _ in "some runs here were reconstructed" }
        let neverSpeaks: (HistoryAutonomyProvenance) -> String? = { _ in nil }
        XCTAssertEqual(alwaysSpeaks(allLive), alwaysSpeaks(backfilled))
        XCTAssertEqual(neverSpeaks(allLive), neverSpeaks(backfilled))

        XCTAssertNil(AutonomyFormat.reconstructionNote(allLive, inView: 100, timeZone: utc))
        XCTAssertNotNil(AutonomyFormat.reconstructionNote(backfilled, inView: 100, timeZone: utc))
    }

    // MARK: The legend

    func testLegendIsTheMeasuredReasonsWhenEveryReasonIsNamed() {
        let spans = [
            row(reason: "ready"),
            row(reason: "waiting"),
            row(reason: "error"),
        ]
        let entries = AutonomyLegend.entries(for: spans)
        XCTAssertEqual(entries.count, AutonomyEndReason.allCases.count)
        XCTAssertFalse(entries.contains(AutonomyLegend.unknown))
    }

    func testLegendGainsTheNeutralEntryForAnUnknownRun() {
        let entries = AutonomyLegend.entries(for: [row(reason: "ready"), row(reason: "unknown")])
        XCTAssertEqual(entries.count, AutonomyEndReason.allCases.count + 1)
        XCTAssertEqual(entries.last, AutonomyLegend.unknown)
        XCTAssertNil(entries.last?.reason, "the neutral entry stands for every unnameable reason, not one of them")
    }

    /// Two different rows draw the same neutral column — a cost-derived span
    /// carrying `unknown`, and an old row written before the reason was
    /// recorded. One entry covers both, or one of them is a colour with no key.
    func testLegendGainsTheNeutralEntryForARowWithNoReasonAtAll() {
        let entries = AutonomyLegend.entries(for: [row(reason: "ready"), row(reason: nil)])
        XCTAssertEqual(entries.count, AutonomyEndReason.allCases.count + 1)
    }

    func testLegendDoesNotInventAnEntryForAnEmptyWindow() {
        XCTAssertEqual(AutonomyLegend.entries(for: []).count, AutonomyEndReason.allCases.count)
    }

    /// An `unknown` span still OCCUPIES its column — the run happened, nothing
    /// can say how it ended — but never outranks a measured reason sharing it.
    func testUnknownOccupiesItsColumnButNeverOutranksAMeasuredReason() {
        let unknownOnly = AutonomyStripLayout.collapse(
            spans: [row(start: 1_000, end: 1_100, reason: "unknown")], start: 0, end: 100_000, columns: 10)
        XCTAssertTrue(unknownOnly[0].occupied, "an unknown run must not read as idle")
        XCTAssertNil(unknownOnly[0].reason)

        let shared = AutonomyStripLayout.collapse(
            spans: [row(start: 1_000, end: 1_100, reason: "unknown"),
                    row(start: 1_200, end: 1_300, reason: "error")],
            start: 0, end: 100_000, columns: 10)
        XCTAssertEqual(shared[0].reason, .error, "one unknown run must not grey out a column holding a real error")
    }

    // MARK: The strip's row cap (QA-1)

    /// The strip had no row cap at all, which nobody could see until the
    /// back-fill gave it history to draw: a 12mo window over a back-filled log
    /// renders 95 project rows into a 380 pt popover. The daemon ranks
    /// `projects` by TOTAL AUTONOMOUS SECONDS, so the cap keeps the rows that
    /// matter and the omission is stated rather than silent.
    func testStripDrawsExactlyTheCapAndAccountsForTheRest() throws {
        let spans = try spansResponse(projectCount: 95)
        XCTAssertEqual(spans.visibleProjects.count, HistoryAutonomySpansResponse.maxStripRows)
        // A PREFIX of the daemon's ranking — the cap must never reorder, or the
        // two clients would disagree about which projects are the busiest.
        XCTAssertEqual(spans.visibleProjects,
                       Array(spans.projects.prefix(HistoryAutonomySpansResponse.maxStripRows)))
        XCTAssertEqual(spans.hiddenProjectCount, 95 - HistoryAutonomySpansResponse.maxStripRows)
        let overflow = try XCTUnwrap(spans.overflowLabel)
        XCTAssertTrue(overflow.contains("+\(95 - HistoryAutonomySpansResponse.maxStripRows) more projects"),
                      "got: \(overflow)")
        // …and WHY they are the missing ones, so the cap does not read as an
        // arbitrary slice of an unknown ordering.
        XCTAssertTrue(overflow.contains("less autonomous time"), "got: \(overflow)")
    }

    func testStripSaysNothingWhenEveryProjectFits() throws {
        let spans = try spansResponse(projectCount: HistoryAutonomySpansResponse.maxStripRows)
        XCTAssertEqual(spans.visibleProjects.count, HistoryAutonomySpansResponse.maxStripRows)
        XCTAssertEqual(spans.hiddenProjectCount, 0)
        XCTAssertNil(spans.overflowLabel)
    }

    func testStripOverflowIsSingularForOneHiddenProject() throws {
        let spans = try spansResponse(projectCount: HistoryAutonomySpansResponse.maxStripRows + 1)
        let overflow = try XCTUnwrap(spans.overflowLabel)
        XCTAssertTrue(overflow.contains("+1 more project,"), "got: \(overflow)")
    }

    /// This surface caps lower than the web's twelve, and that is not a
    /// disagreement: both show a prefix of one shared ranking. What must hold
    /// is that the popover's cap is the smaller one — it has 380 pt to work in.
    func testStripCapFitsThisSurface() {
        XCTAssertGreaterThan(HistoryAutonomySpansResponse.maxStripRows, 0)
        XCTAssertLessThanOrEqual(HistoryAutonomySpansResponse.maxStripRows, 8,
                                 "more rows than a 380 pt popover can show under a 190 pt chart")
    }

    // MARK: Source boundaries on the chart (QA-2)

    func testARangeStraddlingABoundaryMarksIt() throws {
        let d = try durationResponse(bucketStarts: [0, 100, 200, 300, 400],
                                     boundaries: [(100, "cost", "log")])
        XCTAssertEqual(d.visibleBoundaries.map(\.ts), [100])
    }

    func testARangeThatDoesNotStraddleABoundaryMarksNothing() throws {
        let before = try durationResponse(bucketStarts: [1000, 1100, 1200],
                                          boundaries: [(100, "cost", "log")])
        XCTAssertTrue(before.visibleBoundaries.isEmpty)
        let after = try durationResponse(bucketStarts: [0, 100, 200],
                                         boundaries: [(9999, "log", "live")])
        XCTAssertTrue(after.visibleBoundaries.isEmpty)
    }

    /// A rule exactly on the axis marks nothing and reads as a chart border.
    func testABoundaryOnEitherEdgeIsNotDrawn() throws {
        let atStart = try durationResponse(bucketStarts: [100, 200, 300],
                                           boundaries: [(100, "cost", "log")])
        XCTAssertTrue(atStart.visibleBoundaries.isEmpty)
        let atEnd = try durationResponse(bucketStarts: [100, 200, 300],
                                         boundaries: [(300, "log", "live")])
        XCTAssertTrue(atEnd.visibleBoundaries.isEmpty)
    }

    func testBothHandoversAreDrawnByTheOneMechanism() throws {
        let d = try durationResponse(bucketStarts: [0, 100, 200, 300, 400],
                                     boundaries: [(100, "cost", "log"), (300, "log", "live")])
        XCTAssertEqual(d.visibleBoundaries.map(\.ts), [100, 300])
    }

    func testAMachineThatWasNeverBackfilledDrawsNoBoundary() throws {
        let empty = try durationResponse(bucketStarts: [0, 100, 200], boundaries: [])
        XCTAssertTrue(empty.visibleBoundaries.isEmpty)
        // …and a payload from a daemon that predates the field.
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[0,100,200],"buckets":[],
         "summary":{"p95":0,"p50":0,"p5":0,"min":0,"max":0,"count":0},
         "sample_floor":20,"earliest_span":0,"total_recorded":0}
        """
        let old = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
        XCTAssertTrue(old.visibleBoundaries.isEmpty)
    }

    func testADegenerateDomainDrawsNoBoundary() throws {
        let single = try durationResponse(bucketStarts: [100], boundaries: [(100, "cost", "log")])
        XCTAssertTrue(single.visibleBoundaries.isEmpty)
        let flat = try durationResponse(bucketStarts: [100, 100], boundaries: [(100, "cost", "log")])
        XCTAssertTrue(flat.visibleBoundaries.isEmpty)
    }

    /// The label has to say the data BEFORE the line is the coarser one, and
    /// name the resolution — which is the whole reason the marker exists.
    func testBoundaryLabelDescribesWhatLiesToTheLeft() {
        XCTAssertEqual(HistoryAutonomyBoundary(ts: 1, from: "cost", to: "log").label,
                       "← cost log · 60s resolution")
        XCTAssertEqual(HistoryAutonomyBoundary(ts: 1, from: "log", to: "live").label,
                       "← event log · rebuilt")
    }

    func testAnUnknownEraStillGetsALabel() {
        XCTAssertEqual(HistoryAutonomyBoundary(ts: 1, from: "some-future-source", to: "live").label,
                       "← some-future-source")
        XCTAssertEqual(HistoryAutonomyBoundary(ts: 1, from: "", to: "live").label,
                       "← a different source")
    }

    /// Both surfaces must caption a boundary the same way, or one section reads
    /// two different explanations of one artefact. The web's twin table is
    /// AUTONOMY_ERA_LABELS in platforms/web/historyTab.js.
    func testBoundaryLabelsMatchTheWebs() {
        let expected = [
            "cost": "← cost log · 60s resolution",
            "log": "← event log · rebuilt",
            "live": "← measured",
        ]
        for (era, want) in expected {
            XCTAssertEqual(HistoryAutonomyBoundary(ts: 1, from: era, to: "live").label, want)
        }
    }

    /// The committed mutations for both QA fixes, in the idiom the sibling
    /// suites already use. A build that never speaks (the one shipped before
    /// QA-1/QA-2) and one that speaks unconditionally both answer identically
    /// for the two fixtures of each pair; production must not, or each
    /// direction could pass against a build that never looked at the data.
    func testProductionTellsTheCappedAndStraddlingFixturesApart() throws {
        let fits = try spansResponse(projectCount: HistoryAutonomySpansResponse.maxStripRows)
        let overflows = try spansResponse(projectCount: HistoryAutonomySpansResponse.maxStripRows + 1)
        let neverSpeaks: (HistoryAutonomySpansResponse) -> String? = { _ in nil }
        let alwaysSpeaks: (HistoryAutonomySpansResponse) -> String? = { _ in "+N more projects" }
        XCTAssertEqual(neverSpeaks(fits), neverSpeaks(overflows))
        XCTAssertEqual(alwaysSpeaks(fits), alwaysSpeaks(overflows))
        XCTAssertNil(fits.overflowLabel)
        XCTAssertNotNil(overflows.overflowLabel)

        let straddles = try durationResponse(bucketStarts: [0, 100, 200, 300],
                                             boundaries: [(150, "cost", "log")])
        let misses = try durationResponse(bucketStarts: [0, 100, 200, 300],
                                          boundaries: [(9999, "cost", "log")])
        let neverMarks: (HistoryAutonomyDurationResponse) -> Int = { _ in 0 }
        let alwaysMarks: (HistoryAutonomyDurationResponse) -> Int = { _ in 1 }
        XCTAssertEqual(neverMarks(straddles), neverMarks(misses))
        XCTAssertEqual(alwaysMarks(straddles), alwaysMarks(misses))
        XCTAssertEqual(straddles.visibleBoundaries.count, 1)
        XCTAssertEqual(misses.visibleBoundaries.count, 0)
    }

    // MARK: Fixtures

    private func row(start: Int64 = 1, end: Int64 = 9, reason: String?) -> HistoryAutonomySpanRow {
        HistoryAutonomySpanRow(start: start, end: end, project: "p", session: "s", reason: reason)
    }

    /// Decodes a spans payload carrying `projectCount` projects, so the cap is
    /// exercised through the real decode path rather than a hand-built struct.
    private func spansResponse(projectCount: Int) throws -> HistoryAutonomySpansResponse {
        let projects = (0..<projectCount).map { "\"p\($0)\"" }.joined(separator: ",")
        let spans = (0..<projectCount)
            .map { "{\"start\":1,\"end\":9,\"project\":\"p\($0)\",\"session\":\"s\($0)\",\"reason\":\"ready\"}" }
            .joined(separator: ",")
        let json = """
        {"window":"12mo","chart":"autonomy_spans","start":0,"end":100,
         "spans":[\(spans)],"projects":[\(projects)],
         "earliest_span":1,"total_recorded":\(projectCount),"truncated":false}
        """
        return try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(json.utf8))
    }

    private func durationResponse(bucketStarts: [Int64],
                                  boundaries: [(Int64, String, String)]) throws -> HistoryAutonomyDurationResponse {
        let starts = bucketStarts.map(String.init).joined(separator: ",")
        let bounds = boundaries
            .map { "{\"ts\":\($0.0),\"from\":\"\($0.1)\",\"to\":\"\($0.2)\"}" }
            .joined(separator: ",")
        let json = """
        {"window":"1y","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":604800,
         "bucket_starts":[\(starts)],"buckets":[],
         "summary":{"p95":0,"p50":0,"p5":0,"min":0,"max":0,"count":0},
         "sample_floor":20,"earliest_span":0,"total_recorded":0,
         "provenance":{"reconstructed":0,"cost_derived":0,"live_since":0,"boundaries":[\(bounds)]}}
        """
        return try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
    }
}
