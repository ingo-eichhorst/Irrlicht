import XCTest
@testable import Irrlicht

/// The panel's marking for runs whose duration is a FLOOR rather than a
/// measurement (#1905 recording).
///
/// Two kinds, and they are two different limits:
///
///   - STILL RUNNING — the run has not ended, so its length is unknowable.
///     Shown on the strip, deliberately absent from the percentiles.
///   - STARTED BEFORE IRRLICHT WAS WATCHING — the run has finished, but its
///     start is where Irrlicht began watching. Those ARE samples; dropping them
///     is what left 5 of a day's 35 runs on the record.
///
/// A reader who conflated the two would misread the chart in opposite
/// directions, which is why the sentence never merges them.
final class HistoryAutonomyMeasurementTests: XCTestCase {

    // MARK: Decoding

    func testMeasurementAndRowMarksDecode() throws {
        let json = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"done","reason":"ready","kind":"top"},
                  {"start":20,"end":100,"project":"a","session":"live","kind":"top","running":true},
                  {"start":30,"end":60,"project":"a","session":"partial","reason":"unknown",
                   "kind":"top","start_lower_bound":true}],
         "projects":["a"],"earliest_span":1,"total_recorded":3,"truncated":false,
         "measurement":{"running":1,"start_lower_bound":1}}
        """
        let s = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(json.utf8))
        XCTAssertEqual(s.measurementOrNone.running, 1)
        XCTAssertEqual(s.measurementOrNone.lowerBoundStart, 1)

        // A finished, fully measured row claims neither.
        XCTAssertFalse(s.spans[0].isRunning)
        XCTAssertFalse(s.spans[0].hasLowerBoundStart)
        // The in-progress row is marked, and only as running.
        XCTAssertTrue(s.spans[1].isRunning)
        XCTAssertFalse(s.spans[1].hasLowerBoundStart)
        // The unmeasured-start row is marked, and only as that.
        XCTAssertTrue(s.spans[2].hasLowerBoundStart)
        XCTAssertFalse(s.spans[2].isRunning)
    }

    /// A payload from a daemon that predates the field decodes, and reads as
    /// "nothing marked" — the only thing absence can mean, since a build that
    /// could not record a run in progress never wrote one.
    func testAnAbsentMeasurementBlockReadsAsNothingMarked() throws {
        let json = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"buckets":[],
         "summary":{"p95":100,"p50":40,"p5":5,"min":1,"max":100,"count":33},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":33}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(json.utf8))
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

    /// A running run is SHOWN and left OUT of the percentiles, and the sentence
    /// has to say both — otherwise a reader who can see a 3-hour run on the
    /// strip is left wondering why the median did not move.
    func testARunningRunIsNamedAsGoingAndAsAbsentFromThePercentiles() throws {
        let line = try XCTUnwrap(AutonomyFormat.measurementLine(measurement(running: 1)))
        XCTAssertTrue(line.contains("1 run is still going"), line)
        XCTAssertTrue(line.contains("SO FAR"), line)
        XCTAssertTrue(line.contains("left out of the percentiles"), line)
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
