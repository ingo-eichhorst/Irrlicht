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

    func testKindsDecode() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0,"runs":15,"projects":1},
         "earliest_span":1700000000,"total_recorded":15,
         "kinds":{"top_level":7,"subagent":5,"unknown":3}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.kinds?.topLevel, 7)
        XCTAssertEqual(d.kinds?.subagent, 5)
        XCTAssertEqual(d.kinds?.unknown, 3)
    }

    /// The classification's one visible consequence now that the run strip is
    /// gone: a concurrency peak splits only when every run alive at it said
    /// which kind it was. Rows written before the classification carry
    /// `unknown`, and there is no way to recover which they were.
    func testTheSplitIsShownOnlyWhereTheDaemonSaysItIsDerivable() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":0,"end":100,"bucket_seconds":86400,
         "bucket_starts":[0,86400],
         "panels":[{"project":"known","longest":60,"runs":5,"peak":5,"peak_top":3,"peak_sub":2,
                    "peak_split":true,"buckets":[]},
                   {"project":"legacy","longest":60,"runs":5,"peak":5,"peak_top":0,"peak_sub":0,
                    "buckets":[]}],
         "panel_limit":5,"more_projects":0,
         "summary":{"longest":60,"runs":10,"projects":2},
         "earliest_span":1,"total_recorded":10,
         "kinds":{"top_level":3,"subagent":2,"unknown":5}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertTrue(d.panels[0].headline.contains("5 at once (3 + 2 sub)"), d.panels[0].headline)
        XCTAssertTrue(d.panels[1].headline.contains("5 at once"), d.panels[1].headline)
        XCTAssertFalse(d.panels[1].headline.contains("sub"),
                       "a peak an unclassified run was alive for must show the total alone, never a guess")
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
        // …and the clause now carries its consequence for the concurrency
        // figure: a peak one of those runs was alive for cannot be split.
        XCTAssertTrue(line.contains("no split"), line)
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
