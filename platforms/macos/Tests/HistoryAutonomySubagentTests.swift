import XCTest
@testable import Irrlicht

/// What the Autonomy section counts (#1905 subagents), retargeted at the FIELD
/// after the control was removed (#1905 recording).
///
/// The maintainer's decision: every run counts, subagent runs included, because
/// Irrlicht recorded them. So there is no mode, no picker and no excluded
/// count. The classification survives on every row — a subagent's run is still
/// identifiable, and still attributable to the run that contains it — and the
/// panel still describes a window's MAKEUP.
final class HistoryAutonomySubagentTests: XCTestCase {

    // MARK: Decoding

    func testKindsDecodeFromBothPayloads() throws {
        let durationJSON = """
        {"window":"30d","chart":"autonomy_duration","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"buckets":[],
         "summary":{"p95":100,"p50":40,"p5":5,"min":1,"max":100,"count":33},
         "sample_floor":20,"earliest_span":1700000000,"total_recorded":33,
         "kinds":{"top_level":20,"subagent":9,"unknown":4}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyDurationResponse.self, from: Data(durationJSON.utf8))
        let dk = try XCTUnwrap(d.kinds)
        XCTAssertEqual(dk.topLevel, 20)
        XCTAssertEqual(dk.subagent, 9)
        XCTAssertEqual(dk.unknown, 4)

        let spansJSON = """
        {"window":"24h","chart":"autonomy_spans","start":0,"end":100,
         "spans":[{"start":1,"end":9,"project":"a","session":"kid","reason":"ready",
                   "kind":"sub","parent":"boss"},
                  {"start":20,"end":30,"project":"a","session":"boss","reason":"ready","kind":"top"},
                  {"start":40,"end":50,"project":"a","session":"old","reason":"ready","kind":"unknown"}],
         "projects":["a"],"earliest_span":1,"total_recorded":3,"truncated":false,
         "kinds":{"top_level":1,"subagent":1,"unknown":1}}
        """
        let s = try JSONDecoder().decode(HistoryAutonomySpansResponse.self, from: Data(spansJSON.utf8))
        // The subagent's run is RETURNED, and it says so about itself.
        XCTAssertEqual(s.spans.count, 3)
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
        XCTAssertNil(s.spans[0].kind)
        XCTAssertFalse(s.spans[0].isSubagentRun)
        // …and the panel says nothing rather than asserting a census this
        // response never carried.
        XCTAssertNil(AutonomyFormat.countingLine(s.kinds))
    }

    // MARK: The sentence

    private func kinds(topLevel: Int = 10,
                       subagent: Int = 0,
                       unknown: Int = 0) -> HistoryAutonomyKinds {
        HistoryAutonomyKinds(topLevel: topLevel, subagent: subagent, unknown: unknown)
    }

    func testAWindowWithNoSubagentRunsSaysSo() throws {
        let line = try XCTUnwrap(AutonomyFormat.countingLine(kinds()))
        XCTAssertTrue(line.contains("Counting every run"), line)
        XCTAssertTrue(line.contains("holds none"), line)
    }

    /// The word that has to be GONE. Nothing is excluded any more, and a
    /// sentence still claiming so would describe a filter that no longer exists.
    func testItSaysHowManyRunsWereSubagentsAndExcludesNothing() throws {
        let line = try XCTUnwrap(AutonomyFormat.countingLine(kinds(subagent: 37)))
        XCTAssertTrue(line.contains("37 subagent runs"), line)
        XCTAssertTrue(line.contains("inside its parent"), line)
        XCTAssertFalse(line.contains("excluded"), line)
    }

    func testSingularAndPluralBothReadAsEnglish() throws {
        let one = try XCTUnwrap(AutonomyFormat.countingLine(kinds(subagent: 1)))
        XCTAssertTrue(one.contains("1 subagent run —"), one)
        let two = try XCTUnwrap(AutonomyFormat.countingLine(kinds(subagent: 2)))
        XCTAssertTrue(two.contains("2 subagent runs —"), two)
    }

    /// THE TRAP THIS CLAUSE EXISTS FOR. A row written before Irrlicht told the
    /// two apart carries no classification. It is counted like the rest — and
    /// counting it in SILENCE would let the panel imply a classification nobody
    /// made.
    func testUnknownKindRunsAreNamed() throws {
        let line = try XCTUnwrap(AutonomyFormat.countingLine(kinds(subagent: 3, unknown: 8148)))
        XCTAssertTrue(line.contains("8148 runs were recorded before Irrlicht told"), line)
        XCTAssertTrue(line.contains("counted either way"), line)
    }

    func testAWindowWithNoUnknownRunsSaysNothingAboutThem() throws {
        let line = try XCTUnwrap(AutonomyFormat.countingLine(kinds(subagent: 3)))
        XCTAssertFalse(line.contains("unknown"), line)
    }

    /// COMMITTED IN-LANGUAGE MUTANTS, the idiom this suite already uses. Each
    /// is a plausible way to get the sentence wrong and each passes at least one
    /// assertion above on its own, so production has to be shown to tell the
    /// fixtures apart rather than merely to produce a string.
    func testProductionTellsTheCensusCasesApart() throws {
        let withSubs = kinds(subagent: 5, unknown: 9)
        let noSubs = kinds(subagent: 0, unknown: 9)
        let noUnknown = kinds(subagent: 5, unknown: 0)

        // Subagent-blind: a window full of nested runs reads exactly like one
        // with none.
        let subBlind: (HistoryAutonomyKinds) -> String = { _ in "Counting every run." }
        XCTAssertEqual(subBlind(withSubs), subBlind(noSubs))
        XCTAssertNotEqual(AutonomyFormat.countingLine(withSubs), AutonomyFormat.countingLine(noSubs))

        // Unknown-blind: the silent classification.
        let unknownBlind: (HistoryAutonomyKinds) -> String = {
            "Counting every run, including \($0.subagent)."
        }
        XCTAssertEqual(unknownBlind(withSubs), unknownBlind(noUnknown))
        XCTAssertNotEqual(AutonomyFormat.countingLine(withSubs), AutonomyFormat.countingLine(noUnknown))
    }
}
