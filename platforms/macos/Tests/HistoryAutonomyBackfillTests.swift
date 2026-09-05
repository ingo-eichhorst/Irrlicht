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

    func testProvenanceDecodes() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0,"runs":33,"projects":2},
         "earliest_span":1700000000,"total_recorded":33,
         "provenance":{"reconstructed":9,"cost_derived":4,"live_since":1755000000}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.provenanceOrNone.reconstructed, 9)
        XCTAssertEqual(d.provenanceOrNone.costDerived, 4)
        XCTAssertEqual(d.provenanceOrNone.liveSince, 1_755_000_000)
    }

    /// A payload from a daemon that predates the field must still decode. The
    /// alternative is a client that goes blank against an older daemon, which
    /// is a worse failure than saying nothing about provenance.
    func testAnAbsentProvenanceBlockDecodesAsSayingNothing() throws {
        let json = """
        {"window":"1y","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":604800,
         "bucket_starts":[1],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0},"earliest_span":0,"total_recorded":0}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.provenanceOrNone, .none)
        XCTAssertFalse(d.provenanceOrNone.isReconstructed)
        XCTAssertNil(AutonomyFormat.reconstructionNote(d.provenanceOrNone, inView: 0, timeZone: utc))
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

    // MARK: Source boundaries across the panel stack (QA-2)

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
        {"window":"30d","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[0,100,200],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0},"earliest_span":0,"total_recorded":0}
        """
        let old = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
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
    func testProductionTellsTheOverflowAndStraddlingFixturesApart() throws {
        let fits = try projectsResponse(panelCount: 5, moreProjects: 0)
        let overflows = try projectsResponse(panelCount: 5, moreProjects: 7)
        let neverSpeaks: (HistoryAutonomyProjectsResponse) -> String? = { _ in nil }
        let alwaysSpeaks: (HistoryAutonomyProjectsResponse) -> String? = { _ in "+N more projects" }
        XCTAssertEqual(neverSpeaks(fits), neverSpeaks(overflows))
        XCTAssertEqual(alwaysSpeaks(fits), alwaysSpeaks(overflows))
        XCTAssertNil(fits.overflowLabel)
        XCTAssertNotNil(overflows.overflowLabel)

        let straddles = try durationResponse(bucketStarts: [0, 100, 200, 300],
                                             boundaries: [(150, "cost", "log")])
        let misses = try durationResponse(bucketStarts: [0, 100, 200, 300],
                                          boundaries: [(9999, "cost", "log")])
        let neverMarks: (HistoryAutonomyProjectsResponse) -> Int = { _ in 0 }
        let alwaysMarks: (HistoryAutonomyProjectsResponse) -> Int = { _ in 1 }
        XCTAssertEqual(neverMarks(straddles), neverMarks(misses))
        XCTAssertEqual(alwaysMarks(straddles), alwaysMarks(misses))
        XCTAssertEqual(straddles.visibleBoundaries.count, 1)
        XCTAssertEqual(misses.visibleBoundaries.count, 0)
    }

    // MARK: Fixtures

    /// A payload carrying `panelCount` panels and `moreProjects` beyond them,
    /// decoded through the real path rather than hand-built.
    private func projectsResponse(panelCount: Int, moreProjects: Int) throws -> HistoryAutonomyProjectsResponse {
        let panels = (0..<panelCount)
            .map { "{\"project\":\"p\($0)\",\"longest\":60,\"total_seconds\":60,\"runs\":1,\"buckets\":[]}" }
            .joined(separator: ",")
        let json = """
        {"window":"1y","chart":"autonomy_projects","start":0,"end":100,"bucket_seconds":604800,
         "bucket_starts":[0],"panels":[\(panels)],"panel_limit":5,"more_projects":\(moreProjects),
         "summary":{"longest":60,"runs":\(panelCount),"projects":\(panelCount + moreProjects)},
         "earliest_span":1,"total_recorded":\(panelCount)}
        """
        return try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
    }

    private func durationResponse(bucketStarts: [Int64],
                                  boundaries: [(Int64, String, String)]) throws -> HistoryAutonomyProjectsResponse {
        let starts = bucketStarts.map(String.init).joined(separator: ",")
        let bounds = boundaries
            .map { "{\"ts\":\($0.0),\"from\":\"\($0.1)\",\"to\":\"\($0.2)\"}" }
            .joined(separator: ",")
        let json = """
        {"window":"1y","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":604800,
         "bucket_starts":[\(starts)],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0},"earliest_span":0,"total_recorded":0,
         "provenance":{"reconstructed":0,"cost_derived":0,"live_since":0,"boundaries":[\(bounds)]}}
        """
        return try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
    }
}
