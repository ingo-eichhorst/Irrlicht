import XCTest
@testable import Irrlicht

final class MenuBarStatusRendererTests: XCTestCase {
    func testStateSegmentsUseStablePriorityOrderAndFractions() {
        let sessions = [
            makeSession(id: "1", state: .ready),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .ready),
            makeSession(id: "4", state: .waiting)
        ]

        let segments = MenuBarStatusRenderer.stateSegments(for: sessions)

        XCTAssertEqual(segments.count, 3)
        XCTAssertEqual(segments[0].state, .waiting)
        XCTAssertEqual(segments[0].count, 1)
        XCTAssertEqual(segments[0].fraction, 0.25, accuracy: 0.0001)
        XCTAssertEqual(segments[1].state, .working)
        XCTAssertEqual(segments[1].count, 1)
        XCTAssertEqual(segments[1].fraction, 0.25, accuracy: 0.0001)
        XCTAssertEqual(segments[2].state, .ready)
        XCTAssertEqual(segments[2].count, 2)
        XCTAssertEqual(segments[2].fraction, 0.5, accuracy: 0.0001)
    }

    func testAggregatedGroupSVGUsesPieSlicesWhenMultipleStatesArePresent() {
        let sessions = [
            makeSession(id: "1", state: .waiting),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .ready),
            makeSession(id: "4", state: .ready)
        ]

        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)

        XCTAssertEqual(svg.components(separatedBy: "<path ").count - 1, 3)
        XCTAssertTrue(svg.contains(">4</text>"))
    }

    func testAggregatedGroupSVGFallsBackToSolidCircleForSingleStateProjects() {
        let sessions = [
            makeSession(id: "1", state: .working),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .working),
            makeSession(id: "4", state: .working)
        ]

        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)

        XCTAssertEqual(svg.components(separatedBy: "<path ").count - 1, 0)
        XCTAssertEqual(svg.components(separatedBy: "<circle ").count - 1, 1)
        XCTAssertTrue(svg.contains(">4</text>"))
    }

    // #1797 — a group of nothing but unrecognized sessions must not report
    // "all done" in the menu bar.
    //
    // What this test actually guards is `State.dominant(in:)`, whose final
    // `return .ready` used to answer green for an all-unknown collection.
    // Mutation check (verified, not asserted): revert `dominant` to
    // `return .ready` and this goes red.
    //
    // It does NOT guard `segmentOrder` — dropping `.unknown` from that list
    // leaves this test green, because the single-segment `??` fallback also
    // routes to `.unknown`. `testStateSegmentsCoverEveryUnknownStateSession`
    // below is the one that covers `segmentOrder`. Recording the split
    // explicitly because the obvious reading of these two tests gets it
    // backwards.
    func testAggregatedGroupSVGNeverPaintsUnknownSessionsGreen() {
        let sessions = (1...3).map { makeSession(id: "\($0)", state: .unknown) }

        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)

        XCTAssertFalse(
            svg.contains(IrrSVG.ready),
            "a group of only unrecognized sessions must not render ready-green: \(svg)"
        )
        XCTAssertTrue(svg.contains(IrrSVG.unknown), "expected the neutral hue: \(svg)")
        XCTAssertTrue(svg.contains(">3</text>"))
    }

    // #1797 — the universal form of the invariant, stated over EVERY state
    // rather than over `.unknown` specifically: whatever the vocabulary is, the
    // pie must account for all of it. `segmentOrder` derives from `allCases`
    // now, so this holds by construction and would catch a 5th state that
    // arrived with a `menuBarRank` but no wedge.
    func testStateSegmentsCoverEveryDeclaredState() {
        let sessions = SessionState.State.allCases.enumerated().map { i, s in
            makeSession(id: "\(i)", state: s)
        }

        let segments = MenuBarStatusRenderer.stateSegments(for: sessions)

        XCTAssertEqual(
            Set(segments.map(\.state)), Set(SessionState.State.allCases),
            "every declared state needs a wedge, or the dot renders with a hole"
        )
        XCTAssertEqual(segments.map(\.fraction).reduce(0, +), 1.0, accuracy: 0.0001)
    }

    // The pie fractions must still sum to the whole circle once unknown
    // sessions are in the mix — otherwise the dot renders with a hole.
    //
    // This is the test that guards `segmentOrder`'s coverage of `.unknown`
    // specifically (#1797). Mutation check (verified): pin segmentOrder back to
    // a literal `[.waiting, .working, .ready]` and this goes red with count
    // 2-of-4 and fractions summing to 0.5.
    func testStateSegmentsCoverEveryUnknownStateSession() {
        let sessions = [
            makeSession(id: "1", state: .waiting),
            makeSession(id: "2", state: .unknown),
            makeSession(id: "3", state: .ready),
            makeSession(id: "4", state: .unknown)
        ]

        let segments = MenuBarStatusRenderer.stateSegments(for: sessions)

        XCTAssertEqual(segments.map(\.count).reduce(0, +), sessions.count)
        XCTAssertEqual(segments.map(\.fraction).reduce(0, +), 1.0, accuracy: 0.0001)
        // Unknown sorts last, after every state we can actually read.
        XCTAssertEqual(segments.last?.state, .unknown)
        XCTAssertEqual(segments.last?.count, 2)
    }

    func testBuildStatusImageReturnsImageForSessions() {
        let image = MenuBarStatusRenderer.buildStatusImage(
            sessions: [makeSession(id: "1", state: .working)],
            projectGroupOrder: []
        )

        XCTAssertNotNil(image)
    }

    func testBuildStatusSVGOmitsOverflowGlyphAtOrBelowFiveGroups() {
        let sessions = (1...5).map { makeSession(id: "\($0)", state: .working, project: "proj\($0)") }

        let result = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: []
        )

        XCTAssertNotNil(result)
        XCTAssertFalse(result?.svg.contains(">…</text>") ?? true)
    }

    func testBuildStatusSVGAppendsOverflowGlyphBeyondFiveGroups() {
        let sessions = (1...6).map { makeSession(id: "\($0)", state: .working, project: "proj\($0)") }

        let result = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: []
        )

        XCTAssertNotNil(result)
        XCTAssertTrue(result?.svg.contains(">…</text>") ?? false)
    }

    func testBuildStatusSVGReturnsNilForNoSessions() {
        let result = MenuBarStatusRenderer.buildStatusSVG(
            sessions: [],
            projectGroupOrder: []
        )

        XCTAssertNil(result)
    }

    /// The icon takes `SessionManager.projectGroupOrder` verbatim, and since
    /// #1954 that array is a remembered SUPERSET: it holds every project name
    /// ever rendered, including ones with no session right now. Before #1954
    /// the two were kept equal, so no test ever passed a name the sessions do
    /// not mention — checked with
    /// `grep -rn "projectGroupOrder: \[" platforms/macos/Tests/`, whose only
    /// non-empty arguments (`["alpha","beta"]`, `["p"]`) match their sessions
    /// exactly. Reading `orderedProjectGroups` says the miss is skipped by
    /// `remaining.removeValue(forKey:)`; this runs it.
    ///
    /// Both directions, because a superset breaks symmetrically: a remembered
    /// name with no sessions must not draw an empty group, and a session whose
    /// project is not remembered must still be drawn (appended, sorted).
    ///
    /// Mutation-checked, run through `tools/mutate.sh`: replacing
    /// `orderedProjectGroups`' `if let sessions = remaining.removeValue(...)`
    /// with `remaining.removeValue(...) ?? []` — i.e. drawing the misses —
    /// fails this at 46pt against 26pt, the rendered SVG carrying two empty
    /// `<g>` elements and shifting every real dot right.
    func testARememberedNameWithNoSessionsDrawsNothingAndDoesNotDisplaceTheRest() throws {
        let sessions = [
            makeSession(id: "b1", state: .working, project: "beta"),
            makeSession(id: "c1", state: .ready, project: "gamma"),
        ]

        // "alpha" and "delta" are remembered but absent; "gamma" is present
        // but not remembered.
        let superset = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: ["alpha", "beta", "delta"]
        ))
        // The exact render the same sessions produce when the order names only
        // what is on screen — so "identical" is measured, not asserted about
        // an isolated feature of the string.
        let exact = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: ["beta"]
        ))

        XCTAssertEqual(superset.svg, exact.svg,
                       "remembered-but-absent names changed the rendered icon")
        XCTAssertEqual(superset.width, exact.width, accuracy: 0.01,
                       "remembered-but-absent names reserved width for nothing")

        // Vacuity guard. NOT `width > 0`: `assemble` already returns nil on a
        // zero total width, so `XCTUnwrap` would have thrown first and the
        // check could never fire — and a width-only check also waves through
        // the mutation below, which draws two empty `<g>`s at 46pt. Counting
        // the dots is what the equality above cannot already imply.
        XCTAssertEqual(superset.svg.components(separatedBy: "<circle").count - 1, 2,
                       "the icon drew no dots, so the equality above compared two empty renders")
    }

    private func makeSession(
        id: String,
        state: SessionState.State,
        project: String = "test"
    ) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/\(project)",
            projectName: project,
            firstSeen: Date(),
            updatedAt: Date()
        )
    }
}
