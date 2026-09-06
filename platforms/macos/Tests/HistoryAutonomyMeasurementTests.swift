import XCTest
@testable import Irrlicht

/// The panel's marking for runs whose duration is a FLOOR rather than a
/// measurement (#1905 recording).
///
/// A run that has not ended has no length yet — only how long it has lasted SO
/// FAR. That floor COUNTS towards the longest run (the section reports a
/// maximum, and "already lasted 3h" is true), the panel drawing it says "still
/// going", and it is deliberately not a sample for the aggregate chart's
/// percentiles.
///
/// THE SECOND SENTENCE WENT (#1905 prose cut). It named the runs already going
/// when Irrlicht started watching, whose start is a lower bound rather than a
/// beginning. That is a real limit, but it is a restart artefact a reader can
/// do nothing with — and `start_lower_bound` still ships on the wire, so
/// nothing about what the daemon measures changed. The tests that pinned that
/// sentence are retargeted below onto the fact that it no longer speaks.
final class HistoryAutonomyMeasurementTests: XCTestCase {

    // MARK: Decoding

    func testMeasurementAndPanelMarksDecode() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":0,"end":100,"bucket_seconds":86400,
         "bucket_starts":[0],
         "panels":[{"project":"a","longest":10800,"longest_running":true,"total_seconds":10860,
                    "runs":2,"peak":1,
                    "buckets":[{"ts":0,"longest":10800,"running":true,"peak":1}]}],
         "panel_limit":5,"more_projects":0,
         "summary":{"longest":10800,"longest_running":true,"longest_project":"a","runs":2,"projects":1},
         "earliest_span":1,"total_recorded":3,
         "measurement":{"running":1,"start_lower_bound":1}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.measurementOrNone.running, 1)
        XCTAssertEqual(d.measurementOrNone.lowerBoundStart, 1)
        // A run in progress IS the longest run, and it is marked — on the panel
        // and on the bucket the line draws it at.
        XCTAssertTrue(d.panels[0].longestRunning)
        XCTAssertTrue(d.panels[0].buckets[0].running)
        XCTAssertTrue(d.summary.longestRunning)
        XCTAssertTrue(d.panels[0].headline.contains("still going"), d.panels[0].headline)
    }

    /// A payload from a daemon that predates the field decodes, and reads as
    /// "nothing marked" — the only thing absence can mean, since a build that
    /// could not record a run in progress never wrote one.
    func testAnAbsentMeasurementBlockReadsAsNothingMarked() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0,"runs":33,"projects":2},
         "earliest_span":1700000000,"total_recorded":33}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertNil(d.measurement)
        XCTAssertEqual(d.measurementOrNone, .none)
        XCTAssertFalse(d.measurementOrNone.any)
        XCTAssertNil(AutonomyFormat.measurementLine(d.measurementOrNone))
    }

    // MARK: The sentence

    private func measurement(running: Int = 0, lowerBound: Int = 0) -> HistoryAutonomyMeasurement {
        HistoryAutonomyMeasurement(running: running, lowerBoundStart: lowerBound)
    }

    /// The quiet case, and the one that must stay quiet: a machine between
    /// sessions, with nothing running.
    func testSaysNothingWhenNoRunIsStillGoing() {
        XCTAssertNil(AutonomyFormat.measurementLine(measurement()))
    }

    /// A running run's figure is a floor, and the sentence has to say so —
    /// otherwise a reader who can see "longest 3h" beside a project is left
    /// unsure whether that figure is finished.
    /// SAME WORDING AS THE WEB's `autonomyMeasurementNote`.
    func testARunningRunIsNamedAndItsLengthCalledASoFar() throws {
        let line = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 1)))
        XCTAssertEqual(line, "1 run still going — length so far.")
    }

    /// `start_lower_bound` still arrives; it just no longer produces prose. A
    /// payload carrying ONLY that is silent, and one carrying both says exactly
    /// what the running-only payload says — which is the check that would fail
    /// if half the old sentence survived.
    func testTheLowerBoundSentenceIsGoneAndTakesNoOtherLineWithIt() throws {
        XCTAssertNil(AutonomyFormat.measurementLine(measurement(lowerBound: 4)))
        XCTAssertEqual(AutonomyFormat.measurementLine(measurement(running: 2, lowerBound: 3)),
                       AutonomyFormat.measurementLine(measurement(running: 2)))
        let both = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 2, lowerBound: 3)))
        XCTAssertFalse(both.contains("minimum"), both)
        XCTAssertFalse(both.contains("watching"), both)
    }

    func testSingularAndPluralBothReadAsEnglish() throws {
        let one = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 1)))
        XCTAssertEqual(one, "1 run still going — length so far.")
        let two = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 2)))
        XCTAssertEqual(two, "2 runs still going — lengths so far.")
    }

    /// COMMITTED IN-LANGUAGE MUTANTS. Each is a plausible way to get this
    /// wrong, and each passes at least one assertion above on its own.
    func testProductionTellsARunningWindowFromAStillOneAndCountsIt() {
        let running = measurement(running: 3)
        let one = measurement(running: 1)
        let clean = measurement()

        // Count-blind: one long-running session reads exactly like a fleet.
        let countBlind: (HistoryAutonomyMeasurement) -> String = { _ in "Some runs are still going." }
        XCTAssertEqual(countBlind(running), countBlind(one))
        XCTAssertNotEqual(AutonomyFormat.measurementLine(running), AutonomyFormat.measurementLine(one))

        // Never-silent: a window with nothing running carries a caveat it does
        // not need, and the caveat stops meaning anything.
        let always: (HistoryAutonomyMeasurement) -> String = { _ in "Some runs are approximate." }
        XCTAssertEqual(always(clean), always(running))
        XCTAssertNil(AutonomyFormat.measurementLine(clean))
    }
}
