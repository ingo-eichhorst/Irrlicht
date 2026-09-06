import XCTest
@testable import Irrlicht

/// What the Autonomy section counts (#1905 subagents), retargeted at the FIELD
/// after the control was removed (#1905 recording).
///
/// The maintainer's decision: every run counts, subagent runs included, because
/// Irrlicht recorded them. So there is no mode, no picker and no excluded
/// count. The classification survives on every row and on the wire — a
/// subagent's run is still identifiable, and still attributable to the run that
/// contains it.
///
/// THE SENTENCE THAT REPORTED THE CENSUS IS GONE (#1905 prose cut). "Counting
/// every run, including 259 subagent runs" is reassurance rather than a caveat,
/// and its `unknown` clause described legacy rows a machine installing Irrlicht
/// today will never hold. What a reader still needs from the nesting is the one
/// place it changes how a NUMBER reads — the concurrency figure — and that is
/// the caveat beside it, pinned below and in HistoryAutonomyTests.
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

    // MARK: What survives of the sentence

    /// The census sentence is gone. The one consequence of nesting that changes
    /// how a figure READS stays, because the `at once` number is otherwise
    /// taken for a count of independent agents: a parent is held `working`
    /// while its subagents run, so one agent with three subagents overlaps as
    /// four. SAME WORDING AS THE WEB's AUTONOMY_CONCURRENCY_CAVEAT.
    func testTheCaveatIsWhatSurvivesOfTheNestingProse() {
        let caveat = AutonomyFormat.concurrencyCaveat
        XCTAssertTrue(caveat.contains("parent"), caveat)
        XCTAssertTrue(caveat.contains("subagents"), caveat)
        XCTAssertTrue(caveat.contains("working"), caveat)
        // The census wording itself must NOT be back: it is the paragraph the
        // cut removed, and a sentence reporting a count is not a caveat.
        XCTAssertFalse(caveat.contains("subagent run"), caveat)
        XCTAssertFalse(caveat.contains("Counting every run"), caveat)
        XCTAssertFalse(caveat.contains("unknown"), caveat)
    }

    /// The classification is still DECODED and still drives the split beside
    /// the peak (`testKindsDecode` and the panel test above) — what went is only
    /// the prose. This pins that the two facts are independent: kinds arrive,
    /// and no sentence reports them.
    func testKindsStillArriveWithNoSentenceReportingThem() throws {
        let json = """
        {"window":"30d","chart":"autonomy_projects","start":1,"end":2,"bucket_seconds":86400,
         "bucket_starts":[1],"panels":[],"panel_limit":5,"more_projects":0,
         "summary":{"longest":0,"runs":15,"projects":1},
         "earliest_span":1700000000,"total_recorded":15,
         "kinds":{"top_level":7,"subagent":5,"unknown":3}}
        """
        let d = try JSONDecoder().decode(HistoryAutonomyProjectsResponse.self, from: Data(json.utf8))
        XCTAssertEqual(d.kinds?.subagent, 5)
        // The provenance block below the charts names the total and the
        // back-fill, and says nothing about the census.
        let line = AutonomyFormat.provenance(earliest: d.earliestSpan, total: d.totalRecorded,
                                             reconstructed: 0, timeZone: TimeZone(identifier: "UTC")!)
        XCTAssertTrue(line.contains("15 runs recorded"), line)
        XCTAssertFalse(line.contains("subagent"), line)
    }
}
