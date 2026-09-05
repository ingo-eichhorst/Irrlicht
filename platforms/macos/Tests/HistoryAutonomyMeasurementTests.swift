import XCTest
@testable import Irrlicht

/// The panel's marking for runs whose duration is a FLOOR rather than a
/// measurement (#1905 recording).
///
/// Two kinds, and they are two different limits:
///
///   - STILL RUNNING — the run has not ended, so its length is how long it has
///     lasted SO FAR. It COUNTS towards the longest run (the section reports a
///     maximum, and "already lasted 3h" is true) and is marked "still going"
///     rather than presented as final.
///   - STARTED BEFORE IRRLICHT WAS WATCHING — the run has finished, but its
///     start is where Irrlicht began watching. Those ARE samples; dropping them
///     is what left 5 of a day's 35 runs on the record.
///
/// A reader who conflated the two would misread the chart in opposite
/// directions, which is why the sentence never merges them.
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

    /// The quiet case, and the one that must stay quiet: a machine whose daemon
    /// has been up all day, every run in view finished and fully measured.
    func testSaysNothingWhenEveryRunIsFinishedAndMeasured() {
        XCTAssertNil(AutonomyFormat.measurementLine(measurement()))
    }

    /// A running run COUNTS towards the longest and is MARKED, and the
    /// sentence has to say both — otherwise a reader who can see "longest 3h"
    /// beside a project is left unsure whether that figure is finished.
    func testARunningRunIsNamedAsGoingAndAsCounted() throws {
        let line = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 1)))
        XCTAssertTrue(line.contains("1 run is still going"), line)
        XCTAssertTrue(line.contains("SO FAR"), line)
        XCTAssertTrue(line.contains("counts towards the longest run"), line)
        // The percentile-era wording must be gone with the percentiles: a
        // sentence claiming an exclusion that no longer happens is an alibi for
        // a wrong number.
        XCTAssertFalse(line.contains("percentile"), line)
    }

    func testAnUnmeasuredStartSaysWhichEndIsTheEstimate() throws {
        let line = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(lowerBound: 4)))
        XCTAssertTrue(line.contains("4 runs already going when Irrlicht started watching"), line)
        XCTAssertTrue(line.contains("not when the run began"), line)
        XCTAssertTrue(line.contains("minimums"), line)
        // It must NOT claim those runs were dropped from the figures: they are
        // finished runs, and they are samples.
        XCTAssertFalse(line.contains("left out of the percentiles"), line)
    }

    func testBothAtOnceReadAsTwoSeparateFacts() throws {
        let line = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 2, lowerBound: 3)))
        XCTAssertTrue(line.contains("2 runs are still going"), line)
        XCTAssertTrue(line.contains("3 runs already going when Irrlicht started watching"), line)
    }

    func testSingularAndPluralBothReadAsEnglish() throws {
        let one = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 1)))
        XCTAssertTrue(one.contains("1 run is still going"), one)
        let two = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 2)))
        XCTAssertTrue(two.contains("2 runs are still going"), two)
        let oneBound = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(lowerBound: 1)))
        XCTAssertTrue(oneBound.contains("that length is a minimum"), oneBound)
        let twoBound = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(lowerBound: 2)))
        XCTAssertTrue(twoBound.contains("those lengths are minimums"), twoBound)
    }

    /// COMMITTED IN-LANGUAGE MUTANTS. Each is a plausible way to get this
    /// wrong, and each passes at least one assertion above on its own.
    func testProductionTellsTheTwoLimitsApartAndBothFromSilence() {
        let running = measurement(running: 3)
        let bounded = measurement(lowerBound: 3)
        let clean = measurement()

        // Merged: a run still going and a run whose start was guessed read
        // identically, so the reader cannot tell which figure to distrust.
        let merged: (HistoryAutonomyMeasurement) -> String = {
            "\($0.running + $0.lowerBoundStart) runs are approximate."
        }
        XCTAssertEqual(merged(running), merged(bounded))
        XCTAssertNotEqual(AutonomyFormat.measurementLine(running),
                          AutonomyFormat.measurementLine(bounded))

        // Never-silent: a fully measured window carries a caveat it does not
        // need, and the caveat stops meaning anything.
        let always: (HistoryAutonomyMeasurement) -> String = { _ in "Some runs are approximate." }
        XCTAssertEqual(always(clean), always(running))
        XCTAssertNil(AutonomyFormat.measurementLine(clean))
    }
}
