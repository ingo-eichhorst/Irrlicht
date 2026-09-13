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

    /// These two used to name the literal 5 (`…AtOrBelowFiveGroups` /
    /// `…BeyondFiveGroups`). Since #1955 that 5 is the DEFAULT of a setting
    /// rather than a hardcoded cap, so what they pin is restated: the
    /// unspecified `maxGroups:` still means exactly `defaultMaxVisibleGroups`,
    /// and that is still 5 — which is the compatibility promise an install
    /// that never opens Settings rests on. Read from the renderer rather than
    /// retyped, so raising the default surfaces here as a real disagreement.
    func testBuildStatusSVGOmitsOverflowGlyphAtOrBelowTheDefaultBudget() {
        let budget = MenuBarStatusRenderer.defaultMaxVisibleGroups
        XCTAssertEqual(budget, 5, "the default budget is the cap that shipped hardcoded")
        let sessions = (1...budget).map {
            makeSession(id: "\($0)", state: .working, project: "proj\($0)")
        }

        let result = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: []
        )

        XCTAssertNotNil(result)
        XCTAssertFalse(result?.svg.contains(">…</text>") ?? true)
    }

    func testBuildStatusSVGAppendsOverflowGlyphBeyondTheDefaultBudget() {
        let budget = MenuBarStatusRenderer.defaultMaxVisibleGroups
        let sessions = (1...(budget + 1)).map {
            makeSession(id: "\($0)", state: .working, project: "proj\($0)")
        }

        let result = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions,
            projectGroupOrder: []
        )

        XCTAssertNotNil(result)
        XCTAssertTrue(result?.svg.contains(">…</text>") ?? false)
    }

    /// The overflow glyph appears exactly ONCE past the budget, whatever the
    /// budget is (#1955 decision 3) — and the boundary moves with the setting
    /// rather than staying pinned at 5.
    ///
    /// Mutation-proved (source): change `buildStatusSVG`'s
    /// `groups.count > budget` to `>=`, or make it append one glyph per hidden
    /// group, and this goes red.
    func testTheOverflowGlyphTracksTheSlotBudgetAndAppearsExactlyOnce() throws {
        for budget in MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum {
            for projects in [budget, budget + 1, budget + 4] {
                let sessions = (1...projects).map {
                    makeSession(id: "\($0)", state: .working, project: "proj\($0)")
                }
                // Read through the same `renderedOutcome` the superset-order
                // contract uses, so "how many groups did the icon draw" is
                // parsed out of the SVG in exactly one place.
                let outcome = Self.renderedOutcome(sessions, [], budget)
                XCTAssertEqual(outcome.hasOverflow, projects > budget,
                               "budget \(budget), \(projects) projects: overflow glyph")
                // And the budget is a cap on GROUPS, not on dots.
                XCTAssertEqual(outcome.renderedGroups, Swift.min(projects, budget),
                               "budget \(budget), \(projects) projects: rendered group count")
                // The glyph appears exactly ONCE, never one per hidden group —
                // which `hasOverflow` alone (a Bool) cannot say.
                let svg = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
                    sessions: sessions, projectGroupOrder: [], maxGroups: budget
                ), "budget \(budget), \(projects) projects must render").svg
                XCTAssertEqual(svg.components(separatedBy: ">…</text>").count - 1,
                               projects > budget ? 1 : 0,
                               "budget \(budget), \(projects) projects: overflow glyph count")
            }
        }
    }

    // MARK: - The slot budget under a REMEMBERED SUPERSET order (#1955 × #1954)
    //
    // #1956 (issue #1954) turns `projectGroupOrder` into a remembered SUPERSET:
    // every project name ever rendered stays in it forever, so it can carry
    // many names with no sessions right now. #1955 turns the hardcoded cap into
    // a user-set slot budget over the same function. Neither change is unsafe
    // alone; together they have one way to go wrong, and it is invisible to
    // either PR's own tests.
    //
    // Verified by reading `MenuBarStatusRenderer.swift` at this branch head:
    // `buildStatusSVG` builds the group list and hands it to `budgeted`, which
    // spends `prefix(budget)` on THAT list and derives `hidden` from THAT
    // list's count; `orderedProjectGroups` appends a name only `if let sessions
    // = remaining.removeValue(forKey: key)` — so a remembered-but-absent name
    // is skipped before either the budget or the overflow test can see it.
    // `projectGroupOrder` reaches the function only as an ORDERING input; its
    // LENGTH reaches neither line. The combined path takes no order and does no
    // budget arithmetic at all (`buildAggregateStatusSVG(sessions:)`).
    //
    // #1955 phase 2 EXTRACTED that spend into `budgeted` so `location` shares
    // it, and `location` cannot reproduce the hazard in the same shape because
    // `buildLocationStatusSVG` receives no name array at all. It receives a
    // daemon LABEL MAP, which can outlive the sessions the same way — a daemon
    // in `offlineDaemons` with every row already gone. The location contract
    // below is that hazard's counterpart, with its own committed mutants.

    /// What the icon must do with a remembered superset order, as a pure check
    /// over a candidate budgeting strategy — so the regression can be RUN as a
    /// committed mutant rather than described in a PR body.
    ///
    /// The two observable facts are read out of the real SVG: how many groups
    /// were drawn, and whether the overflow glyph is there.
    struct SlotOutcome: Equatable {
        let renderedGroups: Int
        let hasOverflow: Bool
    }

    typealias SlotBudgeting = (
        _ sessions: [SessionState], _ order: [String], _ budget: Int
    ) -> SlotOutcome

    /// How many dot-groups an icon drew and whether it overflowed, read out of
    /// the rendered SVG.
    ///
    /// One parser for both bucketings. It was two identical copies until the
    /// review of #1955 phase 2; they can only ever disagree by drifting, and a
    /// drift here would silently weaken whichever contract kept the older
    /// reading. `nil` — no sessions, so no icon — is zero groups and no
    /// overflow.
    static func outcome(fromSVG svg: String?) -> SlotOutcome {
        guard let svg else { return SlotOutcome(renderedGroups: 0, hasOverflow: false) }
        let hasOverflow = svg.contains(">…</text>")
        let groups = svg.components(separatedBy: "<g transform=").count - 1
        // The overflow glyph occupies a `<g>` of its own, so subtract it to get
        // the count of real dot-groups.
        return SlotOutcome(renderedGroups: groups - (hasOverflow ? 1 : 0),
                           hasOverflow: hasOverflow)
    }

    /// The shipped per-project renderer, observed through its own output.
    static func renderedOutcome(
        _ sessions: [SessionState], _ order: [String], _ budget: Int
    ) -> SlotOutcome {
        outcome(fromSVG: MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: order, maxGroups: budget
        )?.svg)
    }

    static func supersetOrderViolations(_ budgeting: SlotBudgeting) -> [String] {
        var found: [String] = []
        func require(_ condition: Bool, _ message: @autoclosure () -> String) {
            if !condition { found.append(message()) }
        }
        func sessions(_ names: [String]) -> [SessionState] {
            names.enumerated().map { index, name in
                MenuBarFixtures.session(id: "s\(index)", project: name)
            }
        }
        // Twenty names nobody has a session for any more — what #1956's
        // remembered order looks like after a few weeks — plus the few that
        // are live right now.
        let ghosts = (0..<20).map { "retired-project-\($0)" }

        // 1. The budget is spent entirely on RENDERED groups. Three live
        //    projects under a 23-name order and a 5-slot budget must draw
        //    three groups, not five, and must NOT claim an overflow.
        let threeLive = ["alpha", "beta", "gamma"]
        let a = budgeting(sessions(threeLive), ghosts + threeLive, 5)
        require(a.renderedGroups == 3,
                "3 live projects under a 23-name remembered order drew "
                    + "\(a.renderedGroups) groups — the budget was spent on remembered names")
        require(!a.hasOverflow,
                "3 live projects under a 23-name remembered order claimed an overflow — "
                    + "the `…` reflects remembered names rather than hidden groups")

        // 2. The overflow marker reflects RENDERED groups only. Eight live
        //    projects under the same order and budget must draw five and one
        //    glyph — the glyph standing for the three hidden LIVE projects,
        //    never for the twenty ghosts.
        let eightLive = (0..<8).map { "live-\($0)" }
        let b = budgeting(sessions(eightLive), ghosts + eightLive, 5)
        require(b.renderedGroups == 5,
                "8 live projects at a 5-slot budget drew \(b.renderedGroups) groups")
        require(b.hasOverflow, "8 live projects at a 5-slot budget must show one `…`")

        // 3. A budget larger than the live count still draws only what is
        //    live — the remembered order must not reserve empty slots.
        let c = budgeting(sessions(threeLive), ghosts + threeLive, 10)
        require(c.renderedGroups == 3 && !c.hasOverflow,
                "a 10-slot budget over 3 live projects drew \(c.renderedGroups) groups, "
                    + "overflow=\(c.hasOverflow)")

        // 4. The degenerate shape #1956 makes reachable: an order that is ALL
        //    ghosts. The icon must draw the live projects the order says
        //    nothing about, not collapse to a bare `…`.
        let d = budgeting(sessions(threeLive), ghosts, 5)
        require(d.renderedGroups == 3 && !d.hasOverflow,
                "an all-ghost remembered order drew \(d.renderedGroups) groups, "
                    + "overflow=\(d.hasOverflow) — the icon collapsed")
        return found
    }

    /// The shipped renderer satisfies it.
    func testTheSlotBudgetIsSpentOnRenderedGroupsNotRememberedNames() {
        XCTAssertEqual(Self.supersetOrderViolations(Self.renderedOutcome), [])
    }

    /// **The byte-identity statement**, which is the strongest form and the one
    /// #1956's own `testARememberedNameWithNoSessionsDrawsNothingAndDoes`
    /// `NotDisplaceTheRest` pins from its side: a remembered superset order
    /// must render EXACTLY what the same order pruned to live names renders,
    /// at every budget.
    func testASupersetOrderRendersByteIdenticallyToItsPrunedForm() throws {
        let live = ["alpha", "beta", "gamma", "delta", "epsilon", "zeta"]
        let ghosts = (0..<20).map { "retired-project-\($0)" }
        let sessions = live.enumerated().map { index, name in
            MenuBarFixtures.session(id: "s\(index)", project: name)
        }
        // Ghosts interleaved, not merely prepended: a prefix-only fixture would
        // pass against an implementation that skipped a fixed head.
        var interleaved: [String] = []
        for (index, name) in live.enumerated() {
            interleaved.append(ghosts[index * 3])
            interleaved.append(ghosts[index * 3 + 1])
            interleaved.append(name)
        }
        for budget in MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum {
            let superset = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
                sessions: sessions, projectGroupOrder: interleaved, maxGroups: budget
            ))
            let pruned = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
                sessions: sessions, projectGroupOrder: live, maxGroups: budget
            ))
            XCTAssertEqual(superset.svg, pruned.svg,
                           "budget \(budget): a remembered superset order must render "
                               + "byte-identically to its pruned form")
            XCTAssertEqual(superset.width, pruned.width, accuracy: 0.01, "budget \(budget) width")
        }
    }

    /// Mutation-proved (committed): the regression #1955 × #1956 could produce,
    /// as a strategy that spends the budget and decides the overflow from the
    /// remembered NAME array instead of from the built group list.
    ///
    /// This is the fixture the hazard asks for. The equivalent SOURCE mutation
    /// was also applied and run — see this test's sibling note in the PR body —
    /// but the source form cannot be committed beside the real implementation,
    /// and a mutation nobody re-runs is a sentence rather than evidence.
    func testTheSupersetOrderMutantsAreAllRejected() {
        let mutants: [(String, SlotBudgeting)] = [
            ("budget and overflow both taken from the remembered name array", { _, order, budget in
                SlotOutcome(renderedGroups: Swift.min(order.count, budget),
                            hasOverflow: order.count > budget)
            }),
            ("only the OVERFLOW test moved to the name array", { sessions, order, budget in
                let real = Self.renderedOutcome(sessions, order, budget)
                return SlotOutcome(renderedGroups: real.renderedGroups,
                                   hasOverflow: order.count > budget)
            }),
            ("only the BUDGET moved — remembered names eat the slots", { sessions, order, budget in
                let real = Self.renderedOutcome(sessions, order, budget)
                let ghosts = order.count - real.renderedGroups
                let left = Swift.max(0, budget - ghosts)
                return SlotOutcome(renderedGroups: Swift.min(real.renderedGroups, left),
                                   hasOverflow: real.renderedGroups > left)
            }),
        ]
        for (name, mutant) in mutants {
            XCTAssertFalse(Self.supersetOrderViolations(mutant).isEmpty,
                           "the mutant '\(name)' passed the superset-order contract "
                               + "unchallenged — the check does not constrain what it claims")
        }
        // Vacuity guard: a checker that flagged everything would satisfy every
        // row above while proving nothing about the shipped renderer.
        XCTAssertEqual(Self.supersetOrderViolations(Self.renderedOutcome), [],
                       "the shipped renderer must not be flagged")
    }

    /// A budget outside `MenuBarMaxProjects`' bounds must be clamped at the
    /// renderer, not honoured.
    ///
    /// Mutation-proved (source), and written from the mutation as RUN rather
    /// than as predicted: replacing `buildStatusSVG`'s
    /// `let budget = MenuBarMaxProjects.clamp(maxGroups)` with
    /// `let budget = maxGroups` and running this suite produced two distinct
    /// failures. A budget of `0` renders every project away and leaves a lone
    /// `…` as the whole icon (measured: a 10pt SVG carrying only the overflow
    /// glyph, against the 26pt one-dot-plus-glyph the minimum produces). A
    /// NEGATIVE budget does not mis-render — it traps: `Fatal error: Can't
    /// take a prefix of negative length from a collection`. So the clamp is
    /// what stands between a hand-edited plist and a crash on launch, which
    /// is a stronger claim than "the icon would look wrong" and is why the
    /// negative rows are here rather than only the zero one.
    func testAnOutOfRangeSlotBudgetIsClampedRatherThanHonoured() throws {
        let sessions = (1...3).map {
            makeSession(id: "\($0)", state: .working, project: "proj\($0)")
        }
        let atMinimum = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: [], maxGroups: MenuBarMaxProjects.minimum
        ))
        for absurd in [0, -1, Int.min + 1] {
            let built = try XCTUnwrap(
                MenuBarStatusRenderer.buildStatusSVG(
                    sessions: sessions, projectGroupOrder: [], maxGroups: absurd
                ),
                "a budget of \(absurd) must still render an icon, not nothing"
            )
            XCTAssertEqual(built.svg, atMinimum.svg,
                           "a budget of \(absurd) must render as the minimum does")
        }
        let atMaximum = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: [], maxGroups: MenuBarMaxProjects.maximum
        ))
        for absurd in [MenuBarMaxProjects.maximum + 1, Int.max] {
            XCTAssertEqual(
                MenuBarStatusRenderer.buildStatusSVG(
                    sessions: sessions, projectGroupOrder: [], maxGroups: absurd
                )?.svg,
                atMaximum.svg,
                "a budget of \(absurd) must render as the maximum does"
            )
        }
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

    // MARK: - Location grouping: one bucket per daemon (#1955 phase 2)
    //
    // Nothing in this section is red-first evidence — `location` had no
    // bucketing to regress, so every assertion here is either a statement of
    // new behaviour or a mutation-proved check. Which one each is, is said on
    // the test.

    /// One bucket per daemon, local first, then daemons by the label the user
    /// reads.
    ///
    /// New behaviour, so there is no "before" to run red. What makes it more
    /// than a restatement of the implementation is that it counts BUCKETS out
    /// of the rendered SVG (`renderedOutcome`, the same parser the project
    /// contract uses) rather than inspecting the private bucketing function,
    /// and that the fixture puts several projects inside one daemon — a
    /// per-PROJECT bucketing would draw six groups where this asserts three.
    func testLocationDrawsOneBucketPerDaemonLocalFirst() throws {
        let outcome = Self.locationOutcome(MenuBarFixtures.acrossThreeLocations(), MenuBarFixtures.threeLocationLabels, 5)
        XCTAssertEqual(outcome.renderedGroups, 3,
                       "six projects across three locations must draw three buckets")
        XCTAssertFalse(outcome.hasOverflow, "three buckets fit a five-slot budget")

        // Order: local, then by label ascending. `alpha-box` < `beta-box`, but
        // the DAEMON IDs sort the other way (`d-alpha` < `d-beta` agrees, so
        // the labels are deliberately reversed below to tell them apart).
        let reversed = ["d-alpha": "zulu-box", "d-beta": "alpha-box"]
        let render = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: MenuBarFixtures.acrossThreeLocations(), daemonLabels: reversed, maxGroups: 5
        ))
        XCTAssertEqual(
            render.accessibilityDescription,
            "Irrlicht — sessions on Local, alpha-box, zulu-box",
            "buckets must order local-first then by LABEL, not by daemon id"
        )
        // An unlabelled daemon falls back to its bare id rather than vanishing.
        let unlabelled = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: MenuBarFixtures.acrossThreeLocations(), daemonLabels: [:], maxGroups: 5
        ))
        XCTAssertEqual(unlabelled.accessibilityDescription,
                       "Irrlicht — sessions on Local, d-alpha, d-beta")
        XCTAssertEqual(unlabelled.width, render.width, accuracy: 0.01,
                       "a missing label must not change what is DRAWN, only what it is called")
    }

    /// A purely remote install has no `Local` bucket at all — the local bucket
    /// exists because sessions are in it, not because the Mac is running.
    func testLocationOmitsTheLocalBucketWhenNothingRunsLocally() throws {
        let remoteOnly = MenuBarFixtures.acrossThreeLocations().filter { $0.daemonID != nil }
        let render = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: remoteOnly, daemonLabels: MenuBarFixtures.threeLocationLabels, maxGroups: 5
        ))
        XCTAssertEqual(render.accessibilityDescription,
                       "Irrlicht — sessions on alpha-box, beta-box")
        XCTAssertNil(
            MenuBarStatusRenderer.buildLocationStatusSVG(
                sessions: [], daemonLabels: MenuBarFixtures.threeLocationLabels
            ),
            "no sessions must render no icon, not an empty one"
        )
    }

    /// Subagents and background agents are not their own bucket, on this path
    /// either — `topLevelSessions` is shared, and this runs it.
    func testLocationCountsTopLevelSessionsOnly() throws {
        let parented = MenuBarFixtures.acrossThreeLocations() + [
            MenuBarFixtures.session(id: "child", project: "alpha-one",
                                    parentSessionId: "sess_a1", daemonID: "d-alpha"),
            MenuBarFixtures.session(id: "childless-daemon", project: "ghost",
                                    parentSessionId: "sess_a1", daemonID: "d-gamma"),
        ]
        let render = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: parented, daemonLabels: MenuBarFixtures.threeLocationLabels, maxGroups: 5
        ))
        XCTAssertEqual(render.accessibilityDescription,
                       "Irrlicht — sessions on Local, alpha-box, beta-box",
                       "a child session must not open a bucket for a fourth daemon")
        let topLevelOnly = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: MenuBarFixtures.acrossThreeLocations(),
            daemonLabels: MenuBarFixtures.threeLocationLabels, maxGroups: 5
        ))
        XCTAssertEqual(render.svg, topLevelOnly.svg,
                       "child sessions changed the rendered icon")
    }

    /// The single `…` stands for hidden BUCKETS, and the spoken description
    /// says how many — so the two halves of the icon cannot disagree about what
    /// was left out.
    func testTheLocationIconNamesOnlyTheBucketsItActuallyDrew() throws {
        let sessions = (0..<6).map {
            MenuBarFixtures.session(id: "d\($0)", project: "p\($0)", daemonID: "d\($0)")
        }
        let labels = Dictionary(uniqueKeysWithValues: (0..<6).map { ("d\($0)", "host-\($0)") })
        let full = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: labels, maxGroups: 6
        ))
        XCTAssertEqual(full.accessibilityDescription,
                       "Irrlicht — sessions on host-0, host-1, host-2, host-3, host-4, host-5")
        XCTAssertFalse(full.svg.contains(">…</text>"), "six buckets fit a six-slot budget")

        let clipped = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: labels, maxGroups: 4
        ))
        XCTAssertEqual(clipped.accessibilityDescription,
                       "Irrlicht — sessions on host-0, host-1, host-2, host-3, 2 more",
                       "the description must name what was drawn and count what was not")
        XCTAssertTrue(clipped.svg.contains(">…</text>"))

        // Singular, because "1 more" reading as "1 mores" is the kind of thing
        // nobody notices until VoiceOver says it.
        let one = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: labels, maxGroups: 5
        ))
        XCTAssertEqual(one.accessibilityDescription,
                       "Irrlicht — sessions on host-0, host-1, host-2, host-3, host-4, 1 more")

        // And the rasterised image carries it — the SVG is not where VoiceOver
        // looks.
        let image = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusImage(
            sessions: sessions, daemonLabels: labels, maxGroups: 4
        ))
        XCTAssertEqual(image.accessibilityDescription, clipped.accessibilityDescription)
        XCTAssertNil(
            MenuBarStatusRenderer.buildStatusImage(
                sessions: sessions, projectGroupOrder: []
            )?.accessibilityDescription,
            "the per-project icon must keep describing nothing — its buckets are "
                + "already named by the popover"
        )
    }

    // MARK: - The location budget under a daemon map that outlives its rows
    //
    // The location path's counterpart to the superset-order hazard above. The
    // daemon LABEL MAP is the array-that-can-outlive-the-sessions here:
    // `SessionManager.offlineDaemons` keeps a disconnected daemon's label so
    // its faded rows keep their tooltip (#540), and `restoreDaemon` drops that
    // daemon's rows — so a map entry with zero live sessions is an ordinary
    // state, not a corner case. A budget or an overflow test read off the MAP
    // instead of off the built bucket list would show a `…` for daemons that
    // are drawing nothing.

    /// What the location icon must do with a daemon map larger than its live
    /// buckets, as a pure check over a candidate budgeting strategy — so the
    /// regression can be RUN as a committed mutant rather than described.
    typealias LocationBudgeting = (
        _ sessions: [SessionState], _ labels: [String: String], _ budget: Int
    ) -> SlotOutcome

    /// The shipped per-daemon renderer, observed through its own output —
    /// through the SAME parser the per-project one uses, so "how many buckets
    /// did the icon draw" is read in exactly one place.
    static func locationOutcome(
        _ sessions: [SessionState], _ labels: [String: String], _ budget: Int
    ) -> SlotOutcome {
        outcome(fromSVG: MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: labels, maxGroups: budget
        )?.svg)
    }

    static func locationBudgetViolations(_ budgeting: LocationBudgeting) -> [String] {
        var found: [String] = []
        func require(_ condition: Bool, _ message: @autoclosure () -> String) {
            if !condition { found.append(message()) }
        }
        // Twenty daemons this Mac still has names for and no live rows from —
        // what `offlineDaemons` looks like after a few weeks of a flaky link.
        var ghostLabels: [String: String] = [:]
        for index in 0..<20 { ghostLabels["ghost-\(index)"] = "retired-host-\(index)" }

        func sessions(_ daemons: [String?]) -> [SessionState] {
            daemons.enumerated().map { index, id in
                MenuBarFixtures.session(id: "s\(index)", project: "p\(index)", daemonID: id)
            }
        }
        let threeLive: [String?] = [nil, "live-a", "live-b"]
        let liveLabels = ["live-a": "a-box", "live-b": "b-box"]
        let bigMap = ghostLabels.merging(liveLabels) { _, live in live }

        // 1. The budget is spent entirely on RENDERED buckets.
        let a = budgeting(sessions(threeLive), bigMap, 5)
        require(a.renderedGroups == 3,
                "3 live locations under a 22-daemon label map drew \(a.renderedGroups) "
                    + "buckets — the budget was spent on names, not on buckets")
        require(!a.hasOverflow,
                "3 live locations under a 22-daemon label map claimed an overflow")

        // 2. The overflow marker reflects RENDERED buckets only.
        let eightLive: [String?] = (0..<8).map { "live-\($0)" }
        let b = budgeting(sessions(eightLive), bigMap, 5)
        require(b.renderedGroups == 5,
                "8 live locations at a 5-slot budget drew \(b.renderedGroups) buckets")
        require(b.hasOverflow, "8 live locations at a 5-slot budget must show one `…`")

        // 3. A budget larger than the live count reserves no empty slots.
        let c = budgeting(sessions(threeLive), bigMap, 10)
        require(c.renderedGroups == 3 && !c.hasOverflow,
                "a 10-slot budget over 3 live locations drew \(c.renderedGroups) buckets, "
                    + "overflow=\(c.hasOverflow)")

        // 4. The degenerate shape: a map that names nothing currently live.
        //    The icon must draw the live locations the map is silent about.
        let d = budgeting(sessions(threeLive), ghostLabels, 5)
        require(d.renderedGroups == 3 && !d.hasOverflow,
                "an all-ghost daemon map drew \(d.renderedGroups) buckets, "
                    + "overflow=\(d.hasOverflow) — the icon collapsed")
        return found
    }

    /// The shipped renderer satisfies it. New behaviour, not a red-first
    /// regression test; the mutants below are what make it evidence.
    func testTheLocationBudgetIsSpentOnRenderedBucketsNotDaemonLabels() {
        XCTAssertEqual(Self.locationBudgetViolations(Self.locationOutcome), [])
    }

    /// Mutation-proved (committed): three ways the location budget could be
    /// taken from the daemon map instead of from the built bucket list, each
    /// re-run by every suite run rather than described once in a PR body.
    func testTheLocationBudgetMutantsAreAllRejected() {
        let mutants: [(String, LocationBudgeting)] = [
            ("budget and overflow both taken from the daemon label map", { _, labels, budget in
                SlotOutcome(renderedGroups: Swift.min(labels.count, budget),
                            hasOverflow: labels.count > budget)
            }),
            ("only the OVERFLOW test moved to the label map", { sessions, labels, budget in
                let real = Self.locationOutcome(sessions, labels, budget)
                return SlotOutcome(renderedGroups: real.renderedGroups,
                                   hasOverflow: labels.count > budget)
            }),
            ("only the BUDGET moved — named-but-absent daemons eat the slots",
             { sessions, labels, budget in
                let real = Self.locationOutcome(sessions, labels, budget)
                let ghosts = Swift.max(0, labels.count - real.renderedGroups)
                let left = Swift.max(0, budget - ghosts)
                return SlotOutcome(renderedGroups: Swift.min(real.renderedGroups, left),
                                   hasOverflow: real.renderedGroups > left)
            }),
        ]
        for (name, mutant) in mutants {
            XCTAssertFalse(Self.locationBudgetViolations(mutant).isEmpty,
                           "the mutant '\(name)' passed the location-budget contract "
                               + "unchallenged — the check does not constrain what it claims")
        }
        // Vacuity guard: a checker that flagged everything would satisfy every
        // row above while proving nothing about the shipped renderer.
        XCTAssertEqual(Self.locationBudgetViolations(Self.locationOutcome), [],
                       "the shipped renderer must not be flagged")
    }

    /// The whole-icon budget is ONE budget across every bucket, not one per
    /// bucket — the maintainer's decision 3, asserted rather than assumed.
    ///
    /// Eight daemons with three sessions each: a per-bucket budget of 5 would
    /// draw all eight buckets (each is under its own cap); the whole-icon
    /// budget draws five and one `…`.
    func testTheSlotBudgetIsOneBudgetAcrossEveryBucketNotOnePerBucket() throws {
        var sessions: [SessionState] = []
        for daemon in 0..<8 {
            for index in 0..<3 {
                sessions.append(MenuBarFixtures.session(
                    id: "d\(daemon)s\(index)", project: "p\(daemon)-\(index)",
                    daemonID: "d\(daemon)"
                ))
            }
        }
        let outcome = Self.locationOutcome(sessions, [:], 5)
        XCTAssertEqual(outcome.renderedGroups, 5,
                       "the budget must cap the WHOLE icon at five buckets")
        XCTAssertTrue(outcome.hasOverflow)
        let svg = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: [:], maxGroups: 5
        )).svg
        XCTAssertEqual(svg.components(separatedBy: ">…</text>").count - 1, 1,
                       "everything past the budget collapses into ONE `…`, not one per bucket")
    }

    /// A budget outside `MenuBarMaxProjects`' bounds is clamped on this path
    /// too — `budgeted` is shared, and this runs it through the location entry
    /// point rather than assuming the sharing.
    ///
    /// Mutation-proved (source), and written from the mutation as RUN:
    /// replacing `budgeted`'s `let budget = MenuBarMaxProjects.clamp(maxGroups)`
    /// with `let budget = maxGroups` produces both failures the project path's
    /// clamp test records, now on the shared spend. A budget of `0` renders
    /// every bucket away and leaves a lone `…` as the whole icon (measured: a
    /// `width="10"` SVG carrying only the overflow glyph, against the
    /// `width="32"` two-dot-plus-glyph the minimum produces), and a NEGATIVE
    /// budget traps outright — `Swift/Collection.swift:1329: Fatal error:
    /// Can't take a prefix of negative length from a collection`, which the
    /// runner reports as `Exited with unexpected signal code 5`.
    func testAnOutOfRangeLocationBudgetIsClampedRatherThanHonoured() throws {
        let sessions = MenuBarFixtures.acrossThreeLocations()
        let atMinimum = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: [:], maxGroups: MenuBarMaxProjects.minimum
        ))
        for absurd in [0, -1, Int.min + 1] {
            let built = try XCTUnwrap(
                MenuBarStatusRenderer.buildLocationStatusSVG(
                    sessions: sessions, daemonLabels: [:], maxGroups: absurd
                ),
                "a budget of \(absurd) must still render an icon, not nothing"
            )
            XCTAssertEqual(built.svg, atMinimum.svg,
                           "a budget of \(absurd) must render as the minimum does")
        }
        let atMaximum = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sessions, daemonLabels: [:], maxGroups: MenuBarMaxProjects.maximum
        ))
        for absurd in [MenuBarMaxProjects.maximum + 1, Int.max] {
            XCTAssertEqual(
                MenuBarStatusRenderer.buildLocationStatusSVG(
                    sessions: sessions, daemonLabels: [:], maxGroups: absurd
                )?.svg,
                atMaximum.svg,
                "a budget of \(absurd) must render as the maximum does"
            )
        }
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
