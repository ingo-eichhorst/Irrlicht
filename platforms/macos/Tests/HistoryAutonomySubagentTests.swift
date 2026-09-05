import XCTest
@testable import Irrlicht

/// Which runs the Autonomy section counts (#1905 subagents).
///
/// The daemon holds a parent `working` while its children run, so a subagent's
/// span is a NESTED INTERVAL inside its parent's: counting both counts one
/// stretch of wall clock twice and drags the headline median down with short
/// nested runs. Top-level runs only is therefore the default, and the panel has
/// to SAY which mode produced the figures above it.
final class HistoryAutonomySubagentTests: XCTestCase {

    // MARK: The control

    /// The default view asks for NOTHING extra. The daemon's own default is
    /// top-level runs, so an older daemon serving a newer app behaves
    /// identically rather than silently including runs the panel says it
    /// excluded.
    func testOnlyTheIncludingModeSendsAParameter() {
        XCTAssertNil(HistoryAutonomyRunScope.topLevel.includeSubagentsParam)
        XCTAssertEqual(HistoryAutonomyRunScope.all.includeSubagentsParam, "true")
    }

    func testBothModesAreOfferedAndTopLevelIsFirst() {
        XCTAssertEqual(HistoryAutonomyRunScope.allCases.map(\.rawValue), ["top", "all"])
        XCTAssertEqual(HistoryAutonomyRunScope.topLevel.label, "Top-level")
        XCTAssertEqual(HistoryAutonomyRunScope.all.label, "+ subagents")
    }

    // MARK: Decoding

    func testKindsDecodeFromBothPayloads() throws {
        let durationJSON = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"buckets":[],
         "summary":{"p95":100,"p50":40,"p5":5,"min":1,"max":100,"count":33},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":33,
         "kinds":{"mode":"top_level","top_level":20,"subagent":9,"unknown":4}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(durationJSON.utf8))
        XCTAssertEqual(d.kindsOrUnavailable.topLevel, 20)
        XCTAssertEqual(d.kindsOrUnavailable.subagent, 9)
        XCTAssertEqual(d.kindsOrUnavailable.unknown, 4)
        XCTAssertFalse(d.kindsOrUnavailable.includesSubagents)

        let spansJSON = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"kid","reason":"ready",
                   "kind":"sub","parent":"boss"},
                  {"start":20,"end":30,"project":"a","session":"boss","reason":"ready","kind":"top"},
                  {"start":40,"end":50,"project":"a","session":"old","reason":"ready","kind":"unknown"}],
         "projects":["a"],"earliest_span":1,"total_recorded":3,"truncated":false,
         "kinds":{"mode":"all","top_level":1,"subagent":1,"unknown":1}}
        """
        let s = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(spansJSON.utf8))
        XCTAssertTrue(s.kindsOrUnavailable.includesSubagents)
        XCTAssertTrue(s.spans[0].isSubagentRun)
        XCTAssertEqual(s.spans[0].parent, "boss")
        XCTAssertFalse(s.spans[1].isSubagentRun)
        // An `unknown` row is not a subagent run: nothing established it was.
        XCTAssertFalse(s.spans[2].isSubagentRun)
    }

    /// A payload from a daemon that predates the field must still decode, and
    /// a row with no `kind` at all must NOT read as a subagent run — absence is
    /// "nothing established it", never one of the two answers.
    func testAnAbsentKindsBlockDecodesAsSayingNothing() throws {
        let json = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"legacy","reason":"ready"}],
         "projects":["a"],"earliest_span":1,"total_recorded":1,"truncated":false}
        """
        let s = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(json.utf8))
        XCTAssertNil(s.kinds)
        XCTAssertEqual(s.kindsOrUnavailable, .unavailable)
        XCTAssertNil(s.spans[0].kind)
        XCTAssertFalse(s.spans[0].isSubagentRun)
        // …and the panel says nothing rather than asserting a mode this
        // response never stated.
        XCTAssertNil(AutonomyFormat.modeLine(s.kindsOrUnavailable))
    }

    // MARK: The sentence

    private func kinds(mode: String = "top_level",
                       topLevel: Int = 10,
                       subagent: Int = 0,
                       unknown: Int = 0) -> HistoryAutonomyKinds {
        HistoryAutonomyKinds(mode: mode, topLevel: topLevel, subagent: subagent, unknown: unknown)
    }

    func testTheDefaultModeNamesItselfEvenWithNothingToExclude() throws {
        let line = try XCTUnwrap(AutonomyFormat.modeLine(kinds()))
        XCTAssertTrue(line.contains("top-level runs only"), line)
        XCTAssertTrue(line.contains("no subagent runs"), line)
    }

    func testTheDefaultModeSaysHowManyItLeftOut() throws {
        let line = try XCTUnwrap(AutonomyFormat.modeLine(kinds(subagent: 37)))
        XCTAssertTrue(line.contains("37 subagent runs"), line)
        XCTAssertTrue(line.contains("excluded"), line)
    }

    func testSingularAndPluralBothReadAsEnglish() throws {
        let one = try XCTUnwrap(AutonomyFormat.modeLine(kinds(subagent: 1)))
        XCTAssertTrue(one.contains("1 subagent run excluded"), one)
        let two = try XCTUnwrap(AutonomyFormat.modeLine(kinds(subagent: 2)))
        XCTAssertTrue(two.contains("2 subagent runs excluded"), two)
    }

    func testTheIncludingModeSaysWhatItAdded() throws {
        let line = try XCTUnwrap(AutonomyFormat.modeLine(kinds(mode: "all", subagent: 37)))
        XCTAssertTrue(line.contains("Counting every run"), line)
        XCTAssertTrue(line.contains("37 subagent runs"), line)
        XCTAssertFalse(line.contains("excluded"), line)
    }

    /// THE TRAP THIS SENTENCE EXISTS FOR. A row written before Irrlicht told
    /// the two apart carries no classification. It is COUNTED — excluding it
    /// would delete most of a back-filled history on a guess — and counting it
    /// in SILENCE is the failure #1905 exists to prevent: the view would claim
    /// to exclude subagent runs while including every historical one.
    func testUnknownKindRunsAreNamedInBothModes() throws {
        for mode in ["top_level", "all"] {
            let line = try XCTUnwrap(AutonomyFormat.modeLine(kinds(mode: mode, subagent: 3, unknown: 8148)))
            XCTAssertTrue(line.contains("8148 runs were recorded before Irrlicht told"), line)
            XCTAssertTrue(line.contains("counted either way"), line)
        }
    }

    func testAWindowWithNoUnknownRunsSaysNothingAboutThem() throws {
        let line = try XCTUnwrap(AutonomyFormat.modeLine(kinds(subagent: 3)))
        XCTAssertFalse(line.contains("unknown"), line)
    }

    /// COMMITTED IN-LANGUAGE MUTANTS, the idiom this suite already uses. Each
    /// is a plausible way to get the sentence wrong and each passes at least one
    /// assertion above on its own, so production has to be shown to tell the
    /// fixtures apart rather than merely to produce a string.
    func testProductionTellsTheModesAndTheUnknownCaseApart() throws {
        let topOnly = kinds(subagent: 5, unknown: 9)
        let withSubs = kinds(mode: "all", subagent: 5, unknown: 9)
        let noUnknown = kinds(subagent: 5, unknown: 0)

        // Mode-blind: both modes read identically, so a reader could not tell
        // which produced the figures.
        let modeBlind: (HistoryAutonomyKinds) -> String = { "\($0.subagent) subagent runs." }
        XCTAssertEqual(modeBlind(topOnly), modeBlind(withSubs))
        XCTAssertNotEqual(AutonomyFormat.modeLine(topOnly), AutonomyFormat.modeLine(withSubs))

        // Unknown-blind: the silent inclusion.
        let unknownBlind: (HistoryAutonomyKinds) -> String = {
            "Counting top-level runs only · \($0.subagent) excluded."
        }
        XCTAssertEqual(unknownBlind(topOnly), unknownBlind(noUnknown))
        XCTAssertNotEqual(AutonomyFormat.modeLine(topOnly), AutonomyFormat.modeLine(noUnknown))

        // Always-speaks: one sentence for every input.
        let constant: (HistoryAutonomyKinds) -> String = { _ in "Counting runs." }
        XCTAssertEqual(constant(topOnly), constant(withSubs))
    }
}
