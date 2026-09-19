import XCTest
@testable import Irrlicht

/// Issue #1845 — the compact menu bar rendering and the status item's declared
/// autosave name — as reshaped first by #1852, which turned Compact from a
/// fourth `MenuBarStyle` case into an orthogonal Bool, and then by **#1955**,
/// which removed that Bool in favour of the two controls it was a point in:
/// `MenuBarGrouping` (which bucket key the dots use) and `MenuBarMaxProjects`
/// (how many buckets are drawn). Compact IS `combined` grouping with a
/// one-slot budget.
///
/// The file was renamed from `MenuBarCompactStyleTests` rather than deleted:
/// the suite is named for a concept #1955 removes, which is precisely what
/// makes deleting its pins look reasonable. Every one of them is restated
/// against the new model below — the three SOURCE pins in particular
/// (`testImageBuilderRoutesBothHalvesThroughTheExtractedSeams`,
/// `testSourcePinsCannotBeSatisfiedByAComment`,
/// `testTheSourceReaderFailsLoudlyWhenItCannotLook`).
///
/// Nothing here is a regression test: all three issues are enhancements, so
/// there is no defect that ran red on `main`. Every assertion is therefore one
/// of three kinds, and each one says which:
///
/// - **LOCK** — pins behavior that must NOT change. Passes on `main` by
///   construction wherever the symbol it names already exists there. The
///   load-bearing one is
///   `testShippedStylesRenderExactlyWhatTheyDidBeforeAtTheDefaults`: an
///   existing install must render byte-identically until the user opts in.
/// - **Mutation-proved (committed)** — a check this change ADDS, which has no
///   "before". The mutant is committed IN THIS FILE as an alternative
///   implementation and run against the same contract, so the proof is
///   re-executed by every suite run rather than asserted once in a PR body.
///   See the `…Violations` checkers and the `mutants` tables beside them.
/// - **Mutation-proved (source)** — a check whose mutant is a source edit that
///   cannot be expressed in-test (it would not compile beside the real one).
///   The doc comment names the exact edit, written from the mutation as RUN.
@MainActor
final class MenuBarAppearanceTests: XCTestCase {

    // MARK: - Fixtures
    //
    // Shared with MenuBarImageBuilderTests via MenuBarFixtures: both suites
    // assert the same derived widths, so they must assert them against the
    // same sessions (see that file's doc).

    private func makeSession(
        id: String, state: SessionState.State = .working,
        project: String, parentSessionId: String? = nil
    ) -> SessionState {
        MenuBarFixtures.session(id: id, state: state, project: project,
                                parentSessionId: parentSessionId)
    }

    private func sessionsAcrossProjects(_ count: Int) -> [SessionState] {
        MenuBarFixtures.acrossProjects(count)
    }

    private let now = MenuBarFixtures.now

    private func sessionWithQuota() -> SessionState { MenuBarFixtures.sessionWithQuota() }

    /// The appearance under test, at the default slot budget unless a case
    /// cares about it. Spelled once so the `maxProjects:` default cannot drift
    /// between assertions.
    private func appearance(
        _ style: MenuBarStyle,
        _ grouping: MenuBarGrouping,
        maxProjects: Int = MenuBarMaxProjects.defaultValue
    ) -> MenuBarAppearance {
        MenuBarAppearance(style: style, grouping: grouping, maxProjects: maxProjects)
    }

    // MARK: - The styles, and the grouping that is not one of them

    /// LOCK on the compatibility rule: the three shipped styles keep their
    /// raw values and their order, and the default stays `.lights`. A user
    /// who never opens Settings must render exactly as before.
    func testExistingStylesAndDefaultAreUnchanged() {
        XCTAssertEqual(MenuBarStyle.allCases.map(\.rawValue), ["lights", "usage", "combined"])
        XCTAssertEqual(MenuBarStyle(rawValue: "lights"), .lights)
        XCTAssertEqual(MenuBarStyle(rawValue: "usage"), .usage)
        XCTAssertEqual(MenuBarStyle(rawValue: "combined"), .combined)
        // An unset or unknown value still falls back to .lights, so an
        // install that never opts in is untouched — and so is one that
        // downgrades after selecting a style this build does not know.
        XCTAssertEqual(MenuBarStyle(rawValue: "") ?? .lights, .lights)
        XCTAssertEqual(MenuBarStyle(rawValue: "no-such-style") ?? .lights, .lights)
    }

    /// Restates `testCompactIsAModifierAndNotAStyle` against #1955's model:
    /// grouping is a modifier on every style, not a fourth style, and the Bool
    /// it replaced is gone as a setting.
    ///
    /// Mutation-proved (source): re-add `case compact` to `MenuBarStyle` and
    /// the `allCases` assertion goes red; change `MenuBarGrouping.storageKey`
    /// and the key assertion goes red.
    func testGroupingIsAModifierAndNotAStyle() {
        XCTAssertEqual(MenuBarStyle.allCases.count, 3,
                       "density is a modifier — it must not come back as a style")
        XCTAssertNil(MenuBarStyle(rawValue: MenuBarAppearance.legacyCompactStyleRawValue),
                     "\"compact\" must no longer parse as a style")
        XCTAssertEqual(MenuBarGrouping.storageKey, "menuBarGrouping")
        XCTAssertEqual(MenuBarMaxProjects.storageKey, "menuBarMaxProjects")
        // Off unless asked for: an empty store must produce the pre-#1955
        // appearance, which is what the compatibility bound rests on.
        let fresh = MenuBarAppearance.current(in: InMemoryDefaults())
        XCTAssertEqual(fresh.style, .lights)
        XCTAssertEqual(fresh.grouping, .project, "the grouping must default to per-project")
        XCTAssertEqual(fresh.maxProjects, MenuBarStatusRenderer.defaultMaxVisibleGroups,
                       "the slot budget must default to what the renderer hardcoded")
        XCTAssertFalse(fresh.aggregatesSessionDots, "the dense form must default to off")
    }

    /// The grouping's parse/fallback contract, now that phase 2 has
    /// implemented every case.
    ///
    /// Restates `testTheUnimplementedGroupingParsesResolvesAndIsNotOffered`
    /// rather than deleting it: that test's job was to pin the shape of the
    /// gate, and the gate is still there for the NEXT unimplemented grouping.
    /// What flipped is which side of it `location` is on — so the assertions
    /// flip with it instead of disappearing, which is what would make a
    /// silently-unimplemented fourth case possible again.
    ///
    /// The test this replaces ran RED the moment `location.isImplemented`
    /// flipped, which is the gate lifting rather than a defect surfacing:
    /// `("location") is not equal to ("project") - an unimplemented grouping
    /// must fall back to the pre-#1955 bucketing`, `XCTAssertFalse failed -
    /// Settings must not offer a grouping this build cannot render`, and the
    /// `selectableCases` equality. Its assertions are inverted here rather
    /// than removed.
    func testTheLocationGroupingIsImplementedParsesAndIsOffered() throws {
        XCTAssertEqual(MenuBarGrouping.allCases.map(\.rawValue),
                       ["project", "location", "combined"])
        XCTAssertEqual(MenuBarGrouping(rawValue: "location"), .location)
        XCTAssertTrue(MenuBarGrouping.location.isImplemented,
                      "phase 2 gives location a bucket rule of its own")
        XCTAssertEqual(MenuBarGrouping.location.resolved, .location,
                       "an implemented grouping must no longer fall back to project")
        XCTAssertTrue(MenuBarGrouping.selectableCases.contains(.location),
                      "Settings must now offer the grouping this build can render")
        XCTAssertEqual(MenuBarGrouping.selectableCases, [.project, .location, .combined])
        // `selectableCases` is DERIVED — the property that let phase 2 surface
        // the Settings segment without editing `SettingsView`. Still asserted,
        // because it is what a FOURTH grouping will rely on next.
        XCTAssertEqual(MenuBarGrouping.selectableCases,
                       MenuBarGrouping.allCases.filter(\.isImplemented))
        XCTAssertEqual(MenuBarGrouping.location.label, "By location")
        // The budget row's noun. `project` keeps the exact string it had
        // before phase 2 — the default grouping's Settings row must not move.
        XCTAssertEqual(MenuBarGrouping.project.slotBudgetLabel, "Max projects")
        XCTAssertEqual(MenuBarGrouping.location.slotBudgetLabel, "Max locations")
        XCTAssertEqual(Set(MenuBarGrouping.allCases.map(\.slotBudgetLabel)).count,
                       MenuBarGrouping.allCases.count,
                       "two groupings share a budget-row label, so the row cannot say "
                           + "which one is in effect")

        // The gate still works, checked against a case that does not exist:
        // `resolved` must be identity for everything implemented, so the only
        // way to reach the `project` fallback is to be unimplemented.
        for grouping in MenuBarGrouping.allCases {
            XCTAssertEqual(grouping.resolved, grouping.isImplemented ? grouping : .project,
                           "\(grouping): resolved must be identity iff implemented")
        }

        // And `location` no longer renders as `project` does, end to end.
        // Two daemons' worth of sessions spread one-per-project: `project`
        // buckets them by project and `location` by daemon, so the two widths
        // must now DIFFER — the inverse of what this test asserted at phase 1.
        // UNWRAPPED, not `?? -1` / `?? -2`. Those sentinels were safe under the
        // pre-phase-2 `XCTAssertEqual` (two nils gave -1 != -2, so the row went
        // red); inverting the assertion to `XCTAssertNotEqual` inverted the
        // guard too, and two nils would then SATISFY it — review of #1955
        // phase 2 caught that and measured it, with `if true { return nil }`
        // at the top of `dotsImage` leaving the sentinel form green in 0.001s.
        // Re-run against THIS form, that same mutation now fails with
        // `XCTUnwrap failed: expected non-nil value of type "NSImage"`.
        let sessions = MenuBarFixtures.acrossThreeLocations()
        let byProject = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: appearance(.lights, .project),
            sessions: sessions, projectGroupOrder: [], daemonLabels: [:]
        )).size.width
        let byLocation = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: appearance(.lights, .location),
            sessions: sessions, projectGroupOrder: [], daemonLabels: [:]
        )).size.width
        XCTAssertNotEqual(byProject, byLocation,
                          "location must no longer be an alias for project")
    }


    /// The slot budget's bounds are enforced by the TYPE, not only by the
    /// Settings stepper — a hand-edited plist reaches `MenuBarAppearance` too.
    ///
    /// Mutation-proved (committed): see
    /// `testTheSlotBudgetBoundIsMutuallyPinnedWithTheWidthBudget`, whose
    /// mutants move `MenuBarMaxProjects.maximum`.
    func testTheSlotBudgetIsClampedAtEveryConstructionSeam() {
        XCTAssertEqual(MenuBarMaxProjects.minimum, 1)
        XCTAssertEqual(MenuBarMaxProjects.maximum, 10)
        XCTAssertEqual(MenuBarMaxProjects.defaultValue, 5)

        for (raw, want) in [(-7, 1), (0, 1), (1, 1), (5, 5), (10, 10), (11, 10), (9_999, 10)] {
            XCTAssertEqual(MenuBarMaxProjects.clamp(raw), want, "clamp(\(raw))")
            XCTAssertEqual(
                appearance(.lights, .project, maxProjects: raw).maxProjects, want,
                "MenuBarAppearance must clamp \(raw) at construction, not merely refuse to store it"
            )
            let defaults = InMemoryDefaults()
            defaults.set(raw, forKey: MenuBarMaxProjects.storageKey)
            XCTAssertEqual(MenuBarMaxProjects.current(in: defaults), want,
                           "the read seam must clamp \(raw)")
        }

        // Absent is not zero: `integer(forKey:)` answers 0 for both, and they
        // must land on the DEFAULT rather than on the minimum.
        XCTAssertEqual(MenuBarMaxProjects.current(in: InMemoryDefaults()),
                       MenuBarMaxProjects.defaultValue,
                       "an unset budget is 'never chosen', not 'zero'")
    }

    // MARK: - What each appearance renders

    /// The full behaviour matrix — every style × every grouping, which is the
    /// table #1852 specified and #1955 re-parameterised.
    ///
    /// The `.project` column is a **LOCK**: those twelve values are what
    /// `MenuBarStyle`'s four predicates returned on `main` at #1849's merge
    /// with the modifier off, and they must not move. The `.combined` column
    /// is what `isCompact: true` answered at #1852's merge — also a LOCK, and
    /// the reason the migration below can promise an exact carry-over.
    ///
    /// Mutation-proved (source): flip any arm of
    /// `MenuBarAppearance.usesNarrowQuotaBars` or `.aggregatesSessionDots` and
    /// the matching row goes red.
    func testAppearanceRenderingDecisions() {
        // (style, grouping, showsQuotaBars, usesNarrowQuotaBars,
        //  aggregatesSessionDots, hidesDotsWhenQuotaIsRenderable)
        let expected: [(MenuBarStyle, MenuBarGrouping, Bool, Bool, Bool, Bool)] = [
            // ---- LOCK: byte-identical to what each style rendered before ----
            (.lights, .project, false, false, false, false),
            (.usage, .project, true, false, false, true),
            (.combined, .project, true, true, false, false),
            // ---- LOCK: what `isCompact: true` answered at #1852's merge ----
            // lights + combined reproduces #1849's Compact style exactly.
            (.lights, .combined, false, false, true, false),
            // usage + combined reaches the narrow bars the combined STYLE
            // already used — the combination #1845's fourth case could not
            // express at all.
            (.usage, .combined, true, true, true, true),
            // combined style + combined grouping aggregates the dots and
            // leaves the quota half alone; it was already narrow.
            (.combined, .combined, true, true, true, false),
            // ---- #1955 phase 2: location buckets per daemon ----
            // Every predicate answers as the `.project` column does, because
            // location changes the bucket KEY and nothing else — it draws one
            // dot-group per bucket exactly as project does, so it is not the
            // dense form and does not reach the narrow bars on `.usage`. Three
            // rows that duplicate the project column is the point: a
            // `location` that started answering differently would be a
            // grouping quietly turning into a second style.
            (.lights, .location, false, false, false, false),
            (.usage, .location, true, false, false, true),
            (.combined, .location, true, true, false, false),
        ]
        XCTAssertEqual(expected.count,
                       MenuBarStyle.allCases.count * MenuBarGrouping.selectableCases.count,
                       "a style or a selectable grouping was added without its rows here")
        for (style, grouping, quota, narrow, aggregate, hidesDots) in expected {
            let appearance = appearance(style, grouping)
            XCTAssertEqual(appearance.showsQuotaBars, quota, "\(style)/\(grouping) showsQuotaBars")
            XCTAssertEqual(appearance.usesNarrowQuotaBars, narrow,
                           "\(style)/\(grouping) usesNarrowQuotaBars")
            XCTAssertEqual(appearance.aggregatesSessionDots, aggregate,
                           "\(style)/\(grouping) aggregatesSessionDots")
            XCTAssertEqual(appearance.hidesDotsWhenQuotaIsRenderable, hidesDots,
                           "\(style)/\(grouping) hidesDotsWhenQuotaIsRenderable")
        }
    }

    /// Two of the four predicates answer a `style` question only — the
    /// grouping must not move them, and neither must the slot budget. That is
    /// what keeps density a question about HOW DENSELY rather than a second
    /// way to choose content, which is the distinction #1852 established.
    ///
    /// Mutation-proved (source): make `showsQuotaBars` or
    /// `hidesDotsWhenQuotaIsRenderable` read `grouping` and this goes red.
    func testTheGroupingChangesDensityOnlyNeverContent() {
        for style in MenuBarStyle.allCases {
            for grouping in MenuBarGrouping.allCases {
                for budget in [MenuBarMaxProjects.minimum, MenuBarMaxProjects.maximum] {
                    let subject = appearance(style, grouping, maxProjects: budget)
                    let reference = appearance(style, .project)
                    XCTAssertEqual(
                        subject.showsQuotaBars, reference.showsQuotaBars,
                        "\(style)/\(grouping)/\(budget): density must not change WHETHER "
                            + "quota bars are drawn"
                    )
                    XCTAssertEqual(
                        subject.hidesDotsWhenQuotaIsRenderable,
                        reference.hidesDotsWhenQuotaIsRenderable,
                        "\(style)/\(grouping)/\(budget): density must not change whether "
                            + "dots yield to quota"
                    )
                }
            }
        }
    }

    /// Before #1852, `hidesDotsWhenQuotaIsRenderable` happened to equal
    /// `showsQuotaBars && !usesNarrowQuotaBars` across all four styles, and
    /// `testStyleRenderingDecisions` asserted that coincidence so that the day
    /// it stopped holding would be a deliberate, visible decision rather than
    /// a surprise. **That day was #1852**, and this test is the decision:
    /// `usage` + the dense grouping is the counterexample, because it is the
    /// first appearance that both hides its dots and draws narrow bars.
    ///
    /// Kept as an assertion rather than deleted so the derived form cannot
    /// quietly be reintroduced as a "simplification" of the two switches.
    func testTheDerivedEquivalenceNoLongerHolds() {
        let usageDense = appearance(.usage, .combined)
        XCTAssertTrue(usageDense.hidesDotsWhenQuotaIsRenderable)
        XCTAssertTrue(usageDense.usesNarrowQuotaBars)
        XCTAssertNotEqual(
            usageDense.hidesDotsWhenQuotaIsRenderable,
            usageDense.showsQuotaBars && !usageDense.usesNarrowQuotaBars,
            "usage+combined must remain the counterexample that keeps "
                + "hidesDotsWhenQuotaIsRenderable its own exhaustive switch"
        )
    }

    // MARK: - The WIRING of those decisions into the icon
    //
    // Review of #1849 found that every mutation the first round proved red
    // landed on a DEFINITION — the enum's properties, the migration, the
    // renderer. The call sites in `MenuBarImageBuilder` were unpinned, and
    // three separate inversions there passed all 527 tests while visibly
    // changing what a shipped install renders. These tests close that: they
    // exercise the extracted `dotsImage` / `quotaImage` seams, so the routing
    // is asserted rather than only the values it routes.

    /// Each grouping reaches its OWN renderer, and no two of them share one.
    ///
    /// Restates `testDotsImageRoutesTheGroupingToTheAggregateRendererAnd`
    /// `NothingElse`, whose two-way `aggregatesSessionDots ? 18.5 : 90.0`
    /// could not express three destinations. It ran red on this change
    /// (`lights/location`, `usage/location`, `combined/location`: `18.5` is not
    /// equal to `90.0`) precisely because `location` stopped being an alias for
    /// `project` — the failure being the point rather than a problem, since the
    /// old expectation was "location renders as project does".
    ///
    /// Mutation-proved (source), and written from the mutations as RUN rather
    /// than as predicted — the first one failed somewhere other than where it
    /// was expected to:
    ///
    /// - swapping the `.location` and `.combined` arms of
    ///   `MenuBarImageBuilder.dotsImage` fails all SIX rows on the coupling
    ///   assertion (`"the aggregate render and aggregatesSessionDots must
    ///   agree"`), not on a width — the swap moves both destinations at once,
    ///   so the per-grouping widths merely trade places and only the tie to
    ///   the predicate notices;
    /// - pointing the `.location` arm at `buildStatusImage` fails the
    ///   distinctness guard: `("2") is not equal to ("3")`, with
    ///   `[project: 90.0, location: 90.0, combined: 18.5]`.
    ///
    /// The fixture has three locations and six distinct projects, so all three
    /// widths differ; the vacuity guard at the end says so rather than trusting
    /// that they do.
    func testDotsImageRoutesEachGroupingToItsOwnRenderer() {
        let sessions = MenuBarFixtures.acrossThreeLocations()
        let labels = ["d-alpha": "alpha-box", "d-beta": "beta-box"]
        var widthsByGrouping: [MenuBarGrouping: CGFloat] = [:]
        for style in MenuBarStyle.allCases {
            for grouping in MenuBarGrouping.allCases {
                let appearance = appearance(style, grouping)
                let image = MenuBarImageBuilder.dotsImage(
                    appearance: appearance, sessions: sessions,
                    projectGroupOrder: [], daemonLabels: labels
                )
                XCTAssertNotNil(image, "\(style)/\(grouping) must still render dots")
                let width = image?.size.width ?? -1
                // The dot half is a question about the grouping alone: the same
                // grouping must measure the same on every style.
                if let seen = widthsByGrouping[grouping] {
                    XCTAssertEqual(width, seen, accuracy: 0.01,
                                   "\(style)/\(grouping) must not depend on the style")
                } else {
                    widthsByGrouping[grouping] = width
                }
                // …and the aggregate path is still exactly the dense grouping,
                // which is the coupling the switch in `dotsImage` could
                // otherwise drift away from now that it no longer reads the
                // predicate.
                XCTAssertEqual(
                    width == MenuBarStatusRenderer.buildAggregateStatusImage(
                        sessions: sessions
                    )?.size.width,
                    appearance.aggregatesSessionDots,
                    "\(style)/\(grouping): the aggregate render and "
                        + "aggregatesSessionDots must agree"
                )
            }
        }
        XCTAssertEqual(widthsByGrouping.count, MenuBarGrouping.allCases.count,
                       "a grouping went unmeasured — the loop above did not run")
        XCTAssertEqual(Set(widthsByGrouping.values).count, MenuBarGrouping.allCases.count,
                       "two groupings measured the same on this fixture, so the rows "
                           + "above cannot tell their renderers apart: \(widthsByGrouping)")
    }

    // MARK: - Location grouping against the REAL relay ingest (#1955 phase 2)
    //
    // Everything above builds its sessions with `MenuBarFixtures.session` and
    // stamps `daemonID` by hand. That cannot see the ingest: `daemonID` is
    // stamped by `applyRelayInner` from the Push envelope's `source`, the
    // labels come out of `snapshot`/`daemon_status` control frames, and
    // `relaySessionMap` is keyed by `rowID` ("daemon/id") — a suite that seeds
    // those by hand stays green when any of it moves. So the two claims that
    // are actually about the app rather than about the renderer are driven
    // through `RelayFixtures` and a real `SessionManager` here.

    /// Two relay daemons and one un-daemoned session, ingested as frames: the
    /// icon draws exactly one bucket per daemon plus one for local, named by
    /// the labels the relay announced.
    ///
    /// New behaviour, so not red-first. What it adds over the unit-level tests
    /// is that every input comes from the ingest — no `daemonID` is stamped by
    /// the test, and no label is invented by it.
    func testTheLocationGroupingRendersOneBucketPerDaemon() throws {
        let sut = Self.managerWithTwoDaemonsAndOneLocalSession()

        // Vacuity guard: the ingest must actually have produced what the
        // assertions below are about. Without this, a frame shape that stopped
        // parsing would leave every claim here vacuously true.
        XCTAssertEqual(sut.sessions.count, 4, "the four ingested sessions must be live")
        XCTAssertEqual(Set(sut.sessions.map { $0.daemonID }),
                       [nil, "d-alpha", "d-beta"],
                       "the ingest must have stamped the daemon ids the buckets key on")
        XCTAssertEqual(sut.daemonLabels, ["d-alpha": "alpha-box", "d-beta": "beta-box"],
                       "the snapshot frame must have populated the label map")

        let render = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sut.sessions, daemonLabels: sut.daemonLabels,
            maxGroups: MenuBarMaxProjects.defaultValue
        ))
        // ONE bucket per daemon, plus one for local — three, not the four
        // projects the same sessions occupy.
        XCTAssertEqual(render.svg.components(separatedBy: "<g transform=").count - 1, 3,
                       "one bucket per daemon plus one local, not one per project")
        XCTAssertEqual(render.accessibilityDescription,
                       "Irrlicht — sessions on Local, alpha-box, beta-box")
        XCTAssertEqual(sut.apiGroups.count, 4,
                       "the POPOVER must keep its four per-project rows — grouping is an "
                           + "icon-only setting")

        // A daemon that disconnects keeps its bucket and its NAME (fade, don't
        // delete — #540), because `daemonLabels` spans both maps.
        sut.handleRelayMessage(
            RelayFixtures.daemonStatus(daemonID: "d-beta", status: "disconnected")
        )
        XCTAssertEqual(sut.offlineDaemons["d-beta"], "beta-box",
                       "the disconnect frame must have moved the label, not dropped it")
        let faded = try XCTUnwrap(MenuBarStatusRenderer.buildLocationStatusSVG(
            sessions: sut.sessions, daemonLabels: sut.daemonLabels,
            maxGroups: MenuBarMaxProjects.defaultValue
        ))
        XCTAssertEqual(faded.accessibilityDescription, render.accessibilityDescription,
                       "a faded daemon's bucket must keep the name its rows keep, "
                           + "not revert to its id")
    }

    /// **`projectGroupOrder` is byte-identical across a `.location` render** —
    /// the load-bearing constraint, asserted rather than intended.
    ///
    /// That array is project-keyed and popover-owned, and #1956 made it a
    /// remembered SUPERSET that is never pruned. A daemon name written into it
    /// would survive forever and pad the `count - 1` bound
    /// `SessionManager.reorderMoves(atIndex:in:)` measures against, giving the
    /// last visible row a chevron aimed at a row nobody can see — #1948, which
    /// #1949 fixed.
    ///
    /// Mutation-proved (committed): `testTheRememberedOrderRuleRejectsA`
    /// `RendererThatWritesItsBuckets` runs three renderers that break it.
    func testLocationGroupingNeverTouchesTheRememberedProjectOrder() {
        XCTAssertEqual(Self.rememberedOrderViolations(Self.realIconRender), [])
    }

    /// Mutation-proved (committed): three renderers that remember their daemon
    /// buckets the way `SessionManager.orderedGroups` remembers project names.
    /// Each is the mistake the rule above forbids, re-run every suite run.
    func testTheRememberedOrderRuleRejectsARendererThatWritesItsBuckets() {
        let mutants: [(String, IconRender)] = [
            ("appends every daemon LABEL, the way orderedGroups appends a project name",
             { manager, appearance in
                Self.realIconRender(manager, appearance)
                manager.projectGroupOrder += manager.daemonLabels.values.sorted()
                manager.saveProjectGroupOrder()
            }),
            ("appends the LOCAL bucket's name", { manager, appearance in
                Self.realIconRender(manager, appearance)
                manager.projectGroupOrder.append(MenuBarStatusRenderer.localBucketLabel)
                manager.saveProjectGroupOrder()
            }),
            ("reorders the remembered names to match the icon's bucket order",
             { manager, appearance in
                Self.realIconRender(manager, appearance)
                manager.projectGroupOrder.sort()
                manager.saveProjectGroupOrder()
            }),
        ]
        for (name, mutant) in mutants {
            let violations = Self.rememberedOrderViolations(mutant)
            XCTAssertFalse(violations.isEmpty,
                           "the mutant '\(name)' passed the remembered-order rule "
                               + "unchallenged — the check does not constrain what it claims")
        }
        // Vacuity guard: a checker that flagged everything would satisfy every
        // row above while proving nothing about the shipped render path.
        XCTAssertEqual(Self.rememberedOrderViolations(Self.realIconRender), [],
                       "the shipped render path must not be flagged")
    }

    /// One pass of the icon's render, as the production path performs it.
    typealias IconRender = (SessionManager, MenuBarAppearance) -> Void

    /// Exactly what `MenuBarImageBuilder.combinedImage` hands `iconImage` off
    /// the manager. `combinedImage` itself needs a `GasTownProvider` and is
    /// private, so this mirrors its argument list; that the mirror stays
    /// faithful is pinned separately, by
    /// `testImageBuilderRoutesBothHalvesThroughTheExtractedSeams`' source
    /// assertion on `daemonLabels: sessionManager.daemonLabels`.
    static let realIconRender: IconRender = { manager, appearance in
        _ = MenuBarImageBuilder.iconImage(
            appearance: appearance,
            sessions: manager.sessions,
            projectGroupOrder: manager.projectGroupOrder,
            daemonLabels: manager.daemonLabels,
            providerKeys: [],
            now: MenuBarFixtures.now
        )
    }

    /// Renders the icon at every budget under `.location` and reports every way
    /// the remembered project order moved — in memory AND in the store, since
    /// `saveProjectGroupOrder` writes through to defaults and a mutation that
    /// only reached the store would be invisible to the array.
    static func rememberedOrderViolations(_ render: @escaping IconRender) -> [String] {
        var found: [String] = []
        func require(_ condition: Bool, _ message: @autoclosure () -> String) {
            if !condition { found.append(message()) }
        }
        let defaults = InMemoryDefaults()
        let sut = managerWithTwoDaemonsAndOneLocalSession(defaults: defaults)

        let before = sut.projectGroupOrder
        let beforeStored = defaults.stringArray(forKey: sut.projectGroupOrderKey) ?? []
        // Vacuity guard, INSIDE the checker: comparing two empty arrays would
        // satisfy every mutant below. The order must already hold the four
        // ingested project names before anything renders.
        require(Set(before) == ["local-one", "alpha-one", "alpha-two", "beta-one"],
                "the ingest did not populate projectGroupOrder — every comparison "
                    + "below would be vacuous. Got \(before)")

        for budget in MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum {
            render(sut, MenuBarAppearance(style: .lights, grouping: .location,
                                          maxProjects: budget))
            require(sut.projectGroupOrder == before,
                    "budget \(budget): the remembered project order changed across a "
                        + ".location render — \(before) became \(sut.projectGroupOrder)")
            require((defaults.stringArray(forKey: sut.projectGroupOrderKey) ?? [])
                        == beforeStored,
                    "budget \(budget): the PERSISTED project order changed across a "
                        + ".location render")
        }
        // And nothing that names a daemon is in it, however it got there —
        // the equality above only says "unchanged", which a render that wrote
        // a daemon name on the very first pass and then stopped would satisfy
        // from the second pass onward.
        for daemonName in ["d-alpha", "d-beta", "alpha-box", "beta-box",
                           MenuBarStatusRenderer.localBucketLabel] {
            require(!sut.projectGroupOrder.contains(daemonName),
                    "\(daemonName) reached projectGroupOrder — a phantom name in the "
                        + "popover's remembered superset is #1948 in a new costume")
        }
        return found
    }

    /// **The daemon names actually reach the status item's button.**
    ///
    /// Everything else in this file asserts the description on the `NSImage`.
    /// That is the PRODUCER, and it cannot see the one thing that decides
    /// whether a user ever hears the names: an explicit `setAccessibilityLabel`
    /// on the button OVERRIDES its image's `accessibilityDescription`. Review
    /// of this change measured that on a real `NSStatusBarButton` driven the
    /// way `MenuBarController` drives it — the button read `Optional("Irrlicht")`
    /// with the launch-time label in place, and the full bucket sentence
    /// without it. So every description this PR plumbs was dead on arrival, as
    /// `OffFlameImage`'s two already were.
    ///
    /// `MenuBarController.applyIcon` is the fix and this is its test. A plain
    /// `NSButton` rather than a live `NSStatusItem`: `NSStatusBarButton` is an
    /// `NSButton`, the override semantics are the button's, and this needs no
    /// status bar to exist — which keeps it runnable on a headless CI machine.
    ///
    /// Mutation-proved (source), run rather than predicted: dropping the
    /// `setAccessibilityLabel` call from `applyIcon` — i.e. restoring the
    /// pre-fix behaviour, where only `configureStatusItem`'s launch-time label
    /// is ever set — fails the first assertion below.
    func testTheStatusButtonAnnouncesTheIconsOwnDescription() throws {
        let button = NSButton()
        // Exactly what `configureStatusItem` does at launch, once.
        button.setAccessibilityLabel(MenuBarController.defaultAccessibilityLabel)

        let sut = Self.managerWithTwoDaemonsAndOneLocalSession()
        let located = try XCTUnwrap(MenuBarImageBuilder.iconImage(
            appearance: appearance(.lights, .location),
            sessions: sut.sessions, projectGroupOrder: sut.projectGroupOrder,
            daemonLabels: sut.daemonLabels, providerKeys: [], now: now
        ))
        MenuBarController.applyIcon(located, to: button)
        XCTAssertEqual(button.accessibilityLabel(),
                       "Irrlicht — sessions on Local, alpha-box, beta-box",
                       "the launch-time label must not outrank what the icon says "
                           + "about itself — the daemon names have no other surface")
        XCTAssertEqual(button.image, located, "the icon itself must still be applied")

        // Vacuity guard: the label above is not simply whatever was already
        // there. It differs from the default, and an icon that describes
        // NOTHING must fall back to that default rather than keeping a stale
        // sentence from the previous repaint.
        XCTAssertNotEqual(button.accessibilityLabel(),
                          MenuBarController.defaultAccessibilityLabel)
        let perProject = try XCTUnwrap(MenuBarImageBuilder.iconImage(
            appearance: appearance(.lights, .project),
            sessions: sut.sessions, projectGroupOrder: sut.projectGroupOrder,
            daemonLabels: sut.daemonLabels, providerKeys: [], now: now
        ))
        XCTAssertNil(perProject.accessibilityDescription,
                     "this arm is only meaningful while the per-project icon describes nothing")
        MenuBarController.applyIcon(perProject, to: button)
        XCTAssertEqual(button.accessibilityLabel(),
                       MenuBarController.defaultAccessibilityLabel,
                       "switching back to a grouping that names nothing must not leave "
                           + "the previous grouping's daemon names announced")

        // And the flame states, whose descriptions have been set on the image
        // since #593 and reached nobody until `applyIcon` existed.
        MenuBarController.applyIcon(OffFlameImage.menuBar, to: button)
        XCTAssertEqual(button.accessibilityLabel(), "Irrlicht — no active sessions")
        MenuBarController.applyIcon(OffFlameImage.attention, to: button)
        XCTAssertEqual(button.accessibilityLabel(),
                       "Irrlicht — action required: permission pending")

        // …and the repaint path actually GOES THROUGH `applyIcon`. Everything
        // above drives that function directly, which is the only way a test can
        // reach it — `rebuildStatusImage` is private and needs a live
        // `SessionManager`, a `GasTownProvider` and a real status item. So the
        // wiring itself is pinned by reading the source, the idiom
        // `testMenuBarControllerAppliesTheStatusItemIdentity` already uses.
        //
        // Not belt-and-braces: reverting `rebuildStatusImage` to
        // `statusItem.button?.image = MenuBarImageBuilder.build(…)` — the exact
        // line this change replaced — was RUN, and left all 730 tests green
        // while putting the daemon names back beyond VoiceOver's reach. The
        // assertions above cannot see it; this one can.
        let controllerSource = try Self.source(at: Self.menuBarControllerPath)
        XCTAssertTrue(controllerSource.contains("Self.applyIcon("),
                      "rebuildStatusImage must install the icon through applyIcon, or the "
                          + "button keeps its launch-time label and the icon's own "
                          + "description reaches nobody")
        XCTAssertFalse(
            try Self.codeLines(at: Self.menuBarControllerPath)
                .contains("statusItem.button?.image ="),
            "no repaint path may assign the button's image without also re-applying "
                + "its accessibility label — that is the pre-fix shape"
        )
        // Applying them must not have MUTATED the shared statics.
        XCTAssertEqual(OffFlameImage.menuBar.accessibilityDescription,
                       "Irrlicht — no active sessions",
                       "applyIcon must read the shared flame image, never write it")
    }

    /// Four sessions ingested as relay frames: two projects on `d-alpha`, one
    /// on `d-beta`, and one raw daemon frame with no `source` at all — the
    /// `daemonID == nil` shape the `Local` bucket is made of. Plus a `snapshot`
    /// frame giving both daemons the labels the buckets are named by.
    static func managerWithTwoDaemonsAndOneLocalSession(
        defaults: InMemoryDefaults = InMemoryDefaults()
    ) -> SessionManager {
        let sut = SessionManager(defaults: defaults)
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [
            (id: "d-alpha", label: "alpha-box"),
            (id: "d-beta", label: "beta-box"),
        ]))
        sut.handleRelayMessage(RelayFixtures.localFrame(sessionId: "l1", project: "local-one"))
        sut.handleRelayMessage(
            RelayFixtures.push(source: "d-alpha", sessionId: "a1", project: "alpha-one")
        )
        sut.handleRelayMessage(
            RelayFixtures.push(source: "d-alpha", sessionId: "a2", project: "alpha-two")
        )
        sut.handleRelayMessage(
            RelayFixtures.push(source: "d-beta", sessionId: "b1", project: "beta-one")
        )
        return sut
    }

    /// **The slot budget actually reaches the renderer**, which nothing else
    /// here can see: `dotsImage` could ignore `appearance.maxProjects` and
    /// every width assertion above would still pass, because they all sit at
    /// the default.
    ///
    /// Mutation-proved (source): drop `maxGroups: appearance.maxProjects` from
    /// `MenuBarImageBuilder.dotsImage` — reverting it to the renderer's
    /// default — and every row but the 5 row goes red.
    func testTheSlotBudgetReachesTheRendererThroughTheAppearance() throws {
        // Eight one-session projects: enough to overflow every budget below
        // 8, so each row differs from its neighbour.
        let sessions = sessionsAcrossProjects(8)
        let oneDot = MenuBarStatusRenderer.radius * 2 - MenuBarStatusRenderer.overlap
            + MenuBarStatusRenderer.overlap
        var lastWidth: CGFloat = 0
        var widths: [CGFloat] = []
        for budget in MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum {
            let image = try XCTUnwrap(
                MenuBarImageBuilder.dotsImage(
                    appearance: appearance(.lights, .project, maxProjects: budget),
                    sessions: sessions, projectGroupOrder: [], daemonLabels: [:]
                ),
                "budget \(budget) must render dots"
            )
            // Derived from the renderer's own constants, not restated: `budget`
            // one-dot groups (capped at the 8 projects that exist), one
            // overflow glyph while any project is hidden, and a groupGap
            // between each adjacent pair.
            let shown = Swift.min(budget, sessions.count)
            let pieces = shown + (sessions.count > budget ? 1 : 0)
            let expected = CGFloat(shown) * oneDot
                + (sessions.count > budget ? MenuBarStatusRenderer.overflowWidth : 0)
                + CGFloat(pieces - 1) * MenuBarStatusRenderer.groupGap
            XCTAssertEqual(image.size.width, expected, accuracy: 0.01,
                           "budget \(budget) must draw \(shown) groups plus overflow")
            // Non-decreasing, never strictly so per step: the overflow glyph is
            // exactly as wide as a one-dot group here (both 10.00pt, measured
            // by the row above), so the last step before the plateau — swapping
            // the glyph for the group it stood in for — costs nothing. Claiming
            // strict growth per step would be a false statement about the
            // renderer rather than a stronger test.
            XCTAssertGreaterThanOrEqual(image.size.width, lastWidth,
                                        "budget \(budget) must not NARROW the icon")
            lastWidth = image.size.width
            widths.append(image.size.width)
        }
        // But across the whole range the setting does move the icon, which is
        // what "the budget reaches the renderer" means. Without this the
        // per-row equalities would all still hold against a renderer that
        // ignored the argument if the formula above were ever weakened to
        // match it.
        XCTAssertGreaterThan(try XCTUnwrap(widths.last), try XCTUnwrap(widths.first),
                             "the slot budget must change the icon's width across its range")
    }

    /// Mutation-proved (source): swap `usesNarrowQuotaBars` for
    /// `showsQuotaBars` in the `compact:` argument of
    /// `MenuBarImageBuilder.quotaImage`, or invert its `showsQuotaBars` guard,
    /// and this goes red.
    ///
    /// The widths are read from `QuotaMenuBarRenderer`'s own constants rather
    /// than restated, so they cannot drift away from what it renders.
    func testQuotaImageRendersOnlyForTheStylesThatCarryItAndInTheRightLayout() {
        let sessions = [sessionWithQuota()]
        let full = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrow = QuotaMenuBarRenderer.barWidth * QuotaMenuBarRenderer.compactBarWidthFactor

        for style in MenuBarStyle.allCases {
            for grouping in MenuBarGrouping.selectableCases {
                let appearance = appearance(style, grouping)
                let image = MenuBarImageBuilder.quotaImage(
                    appearance: appearance, sessions: sessions, providerKeys: [], now: now
                )
                guard appearance.showsQuotaBars else {
                    XCTAssertNil(image, "\(style)/\(grouping) carries no quota bars")
                    continue
                }
                XCTAssertNotNil(image, "\(style)/\(grouping) must render quota bars")
                let expected = appearance.usesNarrowQuotaBars ? narrow : full
                XCTAssertEqual(image?.size.width ?? -1, expected, accuracy: 0.01,
                               "\(style)/\(grouping) quota layout")
            }
        }
    }

    /// **LOCK — the hard constraint inherited from #1845 through #1852, and
    /// the one criterion that is not negotiable.** An existing install renders
    /// byte-identically at the new keys' defaults.
    ///
    /// This is a lock, not a defect test: it passes on `main` by construction
    /// for the shipped styles, and its whole job is to keep passing. What it
    /// pins is the *numbers*, in points, straight out of the two render
    /// seams — so any reshaping of how those numbers are selected has to
    /// arrive at the same pixels. Note in particular the `.combined` row:
    /// narrow quota bars at the DEFAULT grouping is exactly the behaviour that
    /// would have been lost had density been made to control bar narrowness on
    /// that style (see `MenuBarAppearance.usesNarrowQuotaBars`).
    func testShippedStylesRenderExactlyWhatTheyDidBeforeAtTheDefaults() {
        let sessions = sessionsAcrossProjects(6) + [sessionWithQuota()]
        // Derived from QuotaMenuBarRenderer's constants, not restated — the
        // same rule the per-half tests follow, so a change there surfaces
        // here as a real disagreement rather than as two numbers drifting.
        let full = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrow = QuotaMenuBarRenderer.barWidth * QuotaMenuBarRenderer.compactBarWidthFactor
        let expectations: [(MenuBarStyle, CGFloat?, CGFloat?)] = [
            // style, dots width, quota width (nil = not rendered)
            (.lights, 90.0, nil),
            (.usage, 90.0, full),
            (.combined, 90.0, narrow),
        ]
        XCTAssertEqual(expectations.count, MenuBarStyle.allCases.count,
                       "a style was added without a compatibility row here")
        for (style, dots, quota) in expectations {
            // The DEFAULT appearance, assembled the way an untouched store
            // produces it rather than hand-composed, so a changed default is
            // a failure here rather than something this test papers over.
            let appearance = MenuBarAppearance.current(in: InMemoryDefaults())
            XCTAssertEqual(appearance.grouping, .project)
            XCTAssertEqual(appearance.maxProjects, 5)
            let subject = MenuBarAppearance(
                style: style, grouping: appearance.grouping, maxProjects: appearance.maxProjects
            )
            XCTAssertEqual(
                MenuBarImageBuilder.dotsImage(
                    appearance: subject, sessions: sessions,
                    projectGroupOrder: [], daemonLabels: [:]
                )?.size.width ?? -1,
                dots ?? -1, accuracy: 0.01, "\(style) dots"
            )
            let quotaImage = MenuBarImageBuilder.quotaImage(
                appearance: subject, sessions: sessions, providerKeys: [], now: now
            )
            if let quota {
                XCTAssertEqual(quotaImage?.size.width ?? -1, quota, accuracy: 0.01, "\(style) quota")
            } else {
                XCTAssertNil(quotaImage, "\(style) quota")
            }
        }
    }

    // MARK: - Width and the slot budget (#1845 criterion 2, #1955 decision 3)

    /// The measurement the issues ask for. Not an assertion — it prints the
    /// rendered width of every style × selectable grouping so the figures in
    /// the PR body have a command behind them rather than being typed by hand.
    ///
    ///     cd platforms/macos && swift test --filter testMeasuredWidthOfEachStyle
    ///
    /// Each cell is **derived from the appearance's own predicates**, not
    /// hand-composed per column. That is what stops the printed table and the
    /// shipped behaviour drifting apart: change `usesNarrowQuotaBars` and the
    /// table moves with it, instead of the arithmetic here quietly continuing
    /// to describe the old rules.
    func testMeasuredWidthOfEachStyle() throws {
        // Read from QuotaMenuBarRenderer rather than restated, so the trailing
        // note cannot drift away from what it actually renders.
        let fullQuota = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrowQuota = QuotaMenuBarRenderer.barWidth
            * QuotaMenuBarRenderer.compactBarWidthFactor

        let columns: [(String, MenuBarAppearance)] = MenuBarStyle.allCases.flatMap { style in
            MenuBarGrouping.selectableCases.map { grouping in
                ("\(style.label)/\(grouping == .combined ? "C" : "P")",
                 appearance(style, grouping))
            }
        }
        XCTAssertEqual(columns.count,
                       MenuBarStyle.allCases.count * MenuBarGrouping.selectableCases.count,
                       "every style × selectable grouping must be measured")

        print("=== #1955 measured menu bar icon widths, in points (slot budget "
              + "\(MenuBarMaxProjects.defaultValue)) ===")
        print("projects | " + columns.map { $0.0.padded(to: 10) }.joined(separator: " | "))
        for projects in [1, 2, 3, 5, 6, 8] {
            // Exactly one project carries a renderable quota, so the styles
            // that draw quota bars actually have one to draw.
            let sessions = MenuBarFixtures.acrossProjectsWithQuota(projects)
            let cells = try columns.map { column -> String in
                // XCTUnwrap, not `?? 0`: an icon that regressed to nil would
                // otherwise print 0.00 and pass, making "could not measure"
                // and "measured zero" the same output — the exact failure this
                // file guards against for its source reader.
                let icon = try XCTUnwrap(
                    MenuBarImageBuilder.iconImage(
                        appearance: column.1, sessions: sessions,
                        projectGroupOrder: [], daemonLabels: [:],
                        providerKeys: [], now: now
                    ),
                    "\(column.0) at \(projects) projects rendered no icon"
                )
                return String(format: "%.2f", icon.size.width).padded(to: 10)
            }
            print(String(format: "%8d | ", projects) + cells.joined(separator: " | "))
        }
        print(String(format: "=== measured from MenuBarImageBuilder.iconImage — the same "
                     + "composition the app ships; quota halves are %.2f full / %.2f narrow "
                     + "per QuotaMenuBarRenderer's constants; P = per-project, C = combined ===",
                     fullQuota, narrowQuota))
    }

    /// The widest the dot half can be at a given slot budget, **derived by
    /// reading `MenuBarStatusRenderer`'s own constants** rather than restating
    /// a number that would drift away from them.
    ///
    /// The widest slot **at a two-digit session count** is `aggregateRender`'s
    /// pie-plus-count — `radius * 2 + 2 + 2 * countDigitWidth`. The worst icon
    /// is that slot `budget` times, plus one overflow glyph, with a `groupGap`
    /// between each adjacent pair.
    ///
    /// Two bounds are stated rather than implied, because "the widest a slot
    /// can be" would be false as written:
    /// - `renderCompactGroup`, the other `renderGroup` arm, is capped at 3
    ///   sessions and so at `3 * (radius * 2 - overlap) + overlap` = 22pt,
    ///   under this arm's 25pt. So the aggregate arm is the wider of the two.
    /// - A THREE-digit count in one bucket would be 6.5pt wider still. That is
    ///   not the bound this budget is set against, deliberately: it needs 100+
    ///   sessions inside a single project, ten times over, which is not a
    ///   state the app reaches. The figure is the practical worst case, and
    ///   `testTheWorstCaseFormulaMatchesARealRender` proves it is the width
    ///   the renderer actually produces for the fixture it describes — not
    ///   that no wider icon is expressible.
    static func worstCaseDotWidth(slots: Int) -> CGFloat {
        let widestSlot = MenuBarStatusRenderer.radius * 2 + 2
            + 2 * MenuBarStatusRenderer.countDigitWidth
        let pieceCount = slots + 1  // the slots, plus the overflow glyph
        return CGFloat(slots) * widestSlot
            + MenuBarStatusRenderer.overflowWidth
            + CGFloat(pieceCount - 1) * MenuBarStatusRenderer.groupGap
    }

    /// **The width decision #1955's triage made**, as a ratio rather than a pt
    /// number: the dot half may reach at most twice the worst case it could
    /// reach at today's default budget. Triage's own wording is "about double
    /// today's ~134pt, wide but still representable next to the notch on a 14"
    /// MacBook"; expressing it as a multiple of a figure READ from the
    /// renderer means the budget moves with the constants instead of being a
    /// number typed once.
    static let widthCeilingFactor: CGFloat = 2

    static var widthBudget: CGFloat {
        widthCeilingFactor * worstCaseDotWidth(slots: MenuBarMaxProjects.defaultValue)
    }

    /// The bound, as a pure check over a candidate ceiling — so the mutants
    /// below can be RUN rather than described.
    ///
    /// Two clauses, and they pin the ceiling and the budget to each other:
    /// a ceiling too high for the budget is a violation, and a budget loose
    /// enough to admit one more slot than the ceiling allows is a violation
    /// too. Either one moving alone is therefore red.
    static func slotBudgetViolations(maximum: Int) -> [String] {
        var found: [String] = []
        let atCeiling = worstCaseDotWidth(slots: maximum)
        let oneAbove = worstCaseDotWidth(slots: maximum + 1)
        if atCeiling > widthBudget {
            found.append(String(
                format: "ceiling %d is too wide: %.2fpt worst case against a %.2fpt budget",
                maximum, atCeiling, widthBudget
            ))
        }
        if oneAbove <= widthBudget {
            found.append(String(
                format: "budget %.2fpt admits %d slots (%.2fpt), one more than the ceiling of %d",
                widthBudget, maximum + 1, oneAbove, maximum
            ))
        }
        return found
    }

    /// The real ceiling satisfies the bound, and prints the figures it used.
    func testTheSlotBudgetStaysInsideTheIconsWidth() {
        XCTAssertEqual(
            Self.slotBudgetViolations(maximum: MenuBarMaxProjects.maximum), [],
            "the shipped slot ceiling must sit inside the stated width budget"
        )
        print(String(
            format: "=== #1955 slot budget: default %d -> %.2fpt worst case; "
                + "budget %.2fpt (%.1fx); ceiling %d -> %.2fpt; %d -> %.2fpt (refused) ===",
            MenuBarMaxProjects.defaultValue,
            Self.worstCaseDotWidth(slots: MenuBarMaxProjects.defaultValue),
            Self.widthBudget, Self.widthCeilingFactor,
            MenuBarMaxProjects.maximum,
            Self.worstCaseDotWidth(slots: MenuBarMaxProjects.maximum),
            MenuBarMaxProjects.maximum + 1,
            Self.worstCaseDotWidth(slots: MenuBarMaxProjects.maximum + 1)
        ))
    }

    /// Mutation-proved (committed): every ceiling but the shipped one must be
    /// rejected by `slotBudgetViolations`, and the mutants are run here rather
    /// than described in a PR body.
    ///
    /// `11` is the smallest raise and the one a "just one more" change would
    /// make; `20` and `40` are the shape of "the user should decide"; `9` is
    /// the other direction, where the ceiling drops but the budget does not
    /// follow — which would leave the width decision stated in two places that
    /// no longer agree.
    func testTheSlotBudgetBoundIsMutuallyPinnedWithTheWidthBudget() {
        for mutant in [9, 11, 20, 40] {
            XCTAssertFalse(
                Self.slotBudgetViolations(maximum: mutant).isEmpty,
                "moving the ceiling to \(mutant) must be refused by the width bound, "
                    + "or the bound is not what stops the setting producing a truncated icon"
            )
        }
        // The vacuity guard: a checker that flagged EVERYTHING would satisfy
        // every row above while proving nothing.
        XCTAssertEqual(Self.slotBudgetViolations(maximum: MenuBarMaxProjects.maximum), [],
                       "the shipped ceiling must not be flagged, or the checker flags everything")
    }

    /// The arithmetic `worstCaseDotWidth` states is the arithmetic the
    /// renderer performs — checked against a real render rather than trusted.
    ///
    /// Without this the width budget would be a self-consistent formula that
    /// described nothing: "could not measure" and "measured what I predicted"
    /// would be the same output.
    func testTheWorstCaseFormulaMatchesARealRender() throws {
        let budget = MenuBarMaxProjects.maximum
        // The worst case the formula describes: `budget` + 1 buckets so the
        // overflow glyph appears, each holding a two-digit session count so
        // every slot takes the widest `aggregateRender` form.
        var sessions: [SessionState] = []
        for project in 0...budget {
            for index in 0..<10 {
                sessions.append(makeSession(id: "p\(project)s\(index)", project: "p\(project)"))
            }
        }
        let built = try XCTUnwrap(
            MenuBarStatusRenderer.buildStatusSVG(
                sessions: sessions, projectGroupOrder: [], maxGroups: budget
            ),
            "the worst-case fixture must render"
        )
        XCTAssertEqual(built.width, Self.worstCaseDotWidth(slots: budget), accuracy: 0.01,
                       "the stated worst case must be the width the renderer actually produces")
        XCTAssertTrue(built.svg.contains(">…</text>"),
                      "the worst case must include the overflow glyph the formula charges for")
    }

    /// Mutation-proved (source): make `buildAggregateStatusSVG` group by
    /// project (drop the single `aggregateRender` and call
    /// `orderedProjectGroups`) and this goes red — the width would start
    /// tracking the project count again.
    ///
    /// This is the property #1845 actually asked for: not "narrower" in one
    /// sampled configuration, but *constant* as projects accumulate, which is
    /// what stops the icon sliding behind the notch.
    func testCombinedWidthDependsOnlyOnTheSessionCountsDigits() throws {
        // Every one of these is a one-digit count, so all must be identical.
        let oneDigit = try [1, 2, 3, 5, 9].map { count -> CGFloat in
            try XCTUnwrap(
                MenuBarStatusRenderer.buildAggregateStatusSVG(
                    sessions: sessionsAcrossProjects(count)
                ),
                "aggregate render must produce an icon for \(count) sessions"
            ).width
        }
        XCTAssertEqual(Set(oneDigit).count, 1,
                       "Combined width must not vary with the project count, got \(oneDigit)")

        // Two digits costs exactly one more digit's width, and no more — the
        // width is a function of the count's DIGITS, not of the project
        // count. Naming that precisely matters: at 10 projects the width is
        // legitimately 25.00, and a test claiming "never varies" would read
        // as a regression the first time someone sampled it.
        let twoDigits = try [10, 42, 99].map { count -> CGFloat in
            try XCTUnwrap(MenuBarStatusRenderer.buildAggregateStatusSVG(
                sessions: sessionsAcrossProjects(count)
            )).width
        }
        XCTAssertEqual(Set(twoDigits).count, 1, "got \(twoDigits)")
        XCTAssertEqual(twoDigits[0] - oneDigit[0], MenuBarStatusRenderer.countDigitWidth,
                       accuracy: 0.01,
                       "one extra digit costs exactly countDigitWidth")

        // And the per-project renderer, for contrast, DOES grow — if this ever
        // stops being true the comparison above has lost its meaning.
        let onePro = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessionsAcrossProjects(1), projectGroupOrder: []
        )?.width ?? 0
        let fivePro = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessionsAcrossProjects(5), projectGroupOrder: []
        )?.width ?? 0
        XCTAssertGreaterThan(fivePro, onePro,
                             "the per-project renderer is supposed to widen with more projects")
    }

    /// Mutation-proved (source): same mutation as above. Numeric, in the idiom
    /// `QuotaMenuBarRendererTests.testBuildSVGCompactIsNarrowerThanDefault`
    /// uses — anchored to a stated derivation, not a bare magic number.
    func testCombinedIsNarrowerThanPerProjectOnACrowdedMenuBar() {
        // Six one-session projects: the case #1845 describes.
        let sessions = sessionsAcrossProjects(6)
        let perProject = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: []
        )
        let combined = MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: sessions)
        XCTAssertNotNil(perProject)
        XCTAssertNotNil(combined)
        XCTAssertLessThan(combined!.width, perProject!.width)

        // Combined's width is dot + padding + one digit per digit of the count:
        // radius*2 + 2 + digits*6.5 = 10 + 2 + 6.5 = 18.5 for a 1-digit count.
        XCTAssertEqual(combined!.width, 18.5, accuracy: 0.01)
        // Per-project at six one-session groups: 5 visible groups of one dot
        // (1*(10-4)+4 = 10 each) + an overflow marker (10) + 5 gaps of 6
        // = 50 + 10 + 30 = 90.
        XCTAssertEqual(perProject!.width, 90.0, accuracy: 0.01)
    }

    /// Mutation-proved (source): drop the `topLevelSessions` filter from
    /// `buildAggregateStatusSVG` and this goes red — a subagent would be
    /// counted in the aggregate dot's number even though the per-project
    /// renderer has always excluded it.
    func testCombinedCountsTopLevelSessionsOnly() {
        let sessions = [
            makeSession(id: "p", project: "a"),
            makeSession(id: "c", project: "a", parentSessionId: "p"),
        ]
        let built = MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: sessions)
        XCTAssertNotNil(built)
        XCTAssertTrue(built!.svg.contains(">1<"),
                      "the aggregate label must count only the top-level session, got: \(built!.svg)")
    }

    /// Mutation-proved (source): return a non-nil render for an empty list and
    /// this goes red. An empty aggregate must collapse to "no icon", the same
    /// way `buildStatusSVG`'s `totalWidth > 0` guard does, rather than drawing
    /// a bare "0".
    func testCombinedRendersNothingWithoutSessions() {
        XCTAssertNil(MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: []))
        XCTAssertNil(MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: [
            makeSession(id: "c", project: "a", parentSessionId: "p"),
        ]), "a lone subagent is not a drawable session")
    }

    /// LOCK: the per-project renderer is byte-identical after #1849's shared
    /// `assemble`/`aggregateRender` extraction AND after #1955 parameterised
    /// its cap. This is the hard constraint — an existing install must render
    /// exactly as it did before.
    func testPerProjectRenderingIsUnchangedByTheExtraction() throws {
        let sessions = [
            makeSession(id: "a1", state: .working, project: "alpha"),
            makeSession(id: "a2", state: .ready, project: "alpha"),
            makeSession(id: "b1", state: .error, project: "beta"),
        ]
        let built = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: ["alpha", "beta"]
        ))
        // alpha: 2 overlapping dots = 2*6+4 = 16; beta: 1 dot = 10; gap 6.
        XCTAssertEqual(built.width, 32.0, accuracy: 0.01)

        // The WHOLE string, not just its ends. The group offsets are the
        // statements the shared-assembly extraction moved, so asserting only
        // the prefix/suffix left them unpinned: dropping the `if index > 0`
        // gap rule laid the content out to 38pt while the declared width
        // stayed 32 — clipping the last dot — with the suite green.
        //
        // Deliberately broader than the `contains(...)` idiom the sibling
        // MenuBarStatusRendererTests uses. Those assert FEATURES of the
        // output; this one pins that the extraction changed nothing at all,
        // which is the hard constraint — so any geometry change SHOULD fail
        // here and be re-approved on purpose. The state colors are read from
        // `SessionState.State`, so a palette change does not make it brittle.
        XCTAssertEqual(built.svg, """
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="18">\
        <g transform="translate(0.00,0)">\
        <circle cx="5.00" cy="9.00" r="5.00" fill="#\(SessionState.State.working.hexColor)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>\
        <circle cx="11.00" cy="9.00" r="5.00" fill="#\(SessionState.State.ready.hexColor)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>\
        </g>\
        <g transform="translate(22.00,0)">\
        <circle cx="5.00" cy="9.00" r="5.00" fill="#\(SessionState.State.error.hexColor)" stroke="rgba(0,0,0,0.25)" stroke-width="0.5"/>\
        </g></svg>
        """)

        // And the default slot budget is what an unspecified `maxGroups`
        // means, so the LOCK above measures what a real install renders.
        XCTAssertEqual(
            MenuBarStatusRenderer.buildStatusSVG(
                sessions: sessions, projectGroupOrder: ["alpha", "beta"],
                maxGroups: MenuBarStatusRenderer.defaultMaxVisibleGroups
            )?.svg,
            built.svg,
            "the parameter's default must be the value that was hardcoded"
        )
    }

    /// Mutation-proved (source): in `MenuBarStatusRenderer.image(from:)`,
    /// delete `image.isTemplate = false` or change the size it stamps, and
    /// this goes red. A declared size that disagrees with the SVG canvas
    /// stretches the icon.
    func testRenderedImageCarriesTheSVGsOwnGeometry() throws {
        let sessions = sessionsAcrossProjects(6)
        for (image, expected) in [
            (MenuBarStatusRenderer.buildStatusImage(sessions: sessions, projectGroupOrder: []), 90.0),
            (MenuBarStatusRenderer.buildAggregateStatusImage(sessions: sessions), 18.5),
        ] {
            let img = try XCTUnwrap(image)
            XCTAssertEqual(img.size.width, expected, accuracy: 0.01)
            XCTAssertEqual(img.size.height, 18.0, accuracy: 0.01)
            XCTAssertFalse(img.isTemplate,
                           "the icon is drawn in its own state colors, never tinted as a template")
        }
    }

    // MARK: - Status item autosave name (#1845 criterion 1)

    /// Mutation-proved (source): change the literal in
    /// `MenuBarStatusItemIdentity.autosaveName` and this goes red.
    ///
    /// The name is load-bearing rather than cosmetic: it IS the defaults key
    /// suffix AppKit stores the user's dragged position under, so changing it
    /// in a later release silently orphans every user's position again.
    func testAutosaveNameIsStable() {
        XCTAssertEqual(MenuBarStatusItemIdentity.autosaveName, "IrrlichtStatusItem")
        XCTAssertEqual(
            MenuBarStatusItemIdentity.preferredPositionKey(forAutosaveName: "IrrlichtStatusItem"),
            "NSStatusItem Preferred Position IrrlichtStatusItem"
        )
    }

    /// Mutation-proved (source): delete the `defaults.set(...)` line in
    /// `migrateLegacyPreferredPosition` and this goes red.
    ///
    /// Why it matters: measured on a real install with
    /// `defaults read io.irrlicht.app`, the app's domain already carried
    /// `"NSStatusItem Preferred Position Item-0" = 298` — AppKit's generated
    /// name. Declaring an autosaveName changes which key AppKit reads, so
    /// without this migration every existing install's icon would jump on the
    /// first launch after upgrading.
    func testLegacyPositionIsCarriedOverOnce() {
        let defaults = InMemoryDefaults()
        let legacyKey = MenuBarStatusItemIdentity.preferredPositionKey(forAutosaveName: "Item-0")
        let newKey = MenuBarStatusItemIdentity.preferredPositionKey(
            forAutosaveName: MenuBarStatusItemIdentity.autosaveName
        )
        defaults.set(298, forKey: legacyKey)

        XCTAssertTrue(MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: defaults),
                      "a legacy position with no new key must be carried over")
        XCTAssertEqual(defaults.object(forKey: newKey) as? Int, 298)
        // The legacy key is left alone: a downgrade to a build without an
        // autosaveName then still finds the position it expects.
        XCTAssertEqual(defaults.object(forKey: legacyKey) as? Int, 298)

        // Second launch: nothing left to do, and nothing overwritten.
        XCTAssertFalse(MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: defaults),
                       "the migration must not run twice")
    }

    /// Mutation-proved (source): drop the `defaults.object(forKey: currentKey)
    /// == nil` guard and this goes red — a user who dragged the icon under the
    /// new name would have that position clobbered by a stale legacy value on
    /// every launch.
    func testMigrationNeverOverwritesAPositionSetUnderTheNewName() {
        let defaults = InMemoryDefaults()
        let legacyKey = MenuBarStatusItemIdentity.preferredPositionKey(forAutosaveName: "Item-0")
        let newKey = MenuBarStatusItemIdentity.preferredPositionKey(
            forAutosaveName: MenuBarStatusItemIdentity.autosaveName
        )
        defaults.set(298, forKey: legacyKey)
        defaults.set(42, forKey: newKey)

        XCTAssertFalse(MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: defaults))
        XCTAssertEqual(defaults.object(forKey: newKey) as? Int, 42)
    }

    /// Mutation-proved (source): drop the `let legacyValue = ...` guard and
    /// this goes red. A fresh install has nothing to migrate and must not have
    /// a key invented for it.
    func testMigrationIsANoOpOnAFreshInstall() {
        let defaults = InMemoryDefaults()
        let newKey = MenuBarStatusItemIdentity.preferredPositionKey(
            forAutosaveName: MenuBarStatusItemIdentity.autosaveName
        )
        XCTAssertFalse(MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: defaults))
        XCTAssertNil(defaults.object(forKey: newKey))
    }

    // MARK: - Migrating off both shapes of "compact" (#1955 decision 1)

    /// A migration, as a function of the store — so the mutants below can be
    /// RUN against the same contract the real one is held to.
    typealias StoreMigration = (UserDefaults) -> Bool

    /// The compact migration's whole contract, as a pure check that returns
    /// the failures it found rather than asserting them.
    ///
    /// Written this way for one reason: it is the only shape in which the
    /// mutants can be COMMITTED. A mutation described in a PR body is re-run
    /// by nobody; a mutant run through this checker is re-run by every suite
    /// run, and a checker that stopped checking shows up as a mutant with no
    /// violations rather than as silence.
    static func compactMigrationViolations(_ migrate: StoreMigration) -> [String] {
        var found: [String] = []
        func require(_ condition: Bool, _ message: @autoclosure () -> String) {
            if !condition { found.append(message()) }
        }

        // 1. #1852's Bool: compact ON carries over to combined + one slot, and
        //    the legacy key is left readable for a downgrade.
        let fromBool = InMemoryDefaults()
        fromBool.set(true, forKey: MenuBarAppearance.legacyCompactStorageKey)
        require(migrate(fromBool), "a stored menuBarCompact=true must be carried over")
        let bool = MenuBarAppearance.current(in: fromBool)
        require(bool.grouping == .combined,
                "menuBarCompact=true must become combined grouping, got \(bool.grouping)")
        require(bool.maxProjects == MenuBarMaxProjects.minimum,
                "menuBarCompact=true must become a one-slot budget, got \(bool.maxProjects)")
        require(fromBool.object(forKey: MenuBarAppearance.legacyCompactStorageKey) != nil,
                "the legacy compact key must be left in place for a downgrade")

        // 2. #1849's style: the unparseable raw value is rewritten to lights
        //    AND carried over, because it lives in the key the new value needs.
        let fromStyle = InMemoryDefaults()
        fromStyle.set(MenuBarAppearance.legacyCompactStyleRawValue,
                      forKey: MenuBarStyle.storageKey)
        require(migrate(fromStyle), "a stored Compact STYLE must be carried over")
        let style = MenuBarAppearance.current(in: fromStyle)
        require(style.style == .lights,
                "Compact showed dots and no quota bars — that is Lights, got \(style.style)")
        require(style.grouping == .combined,
                "the Compact style must become combined grouping, got \(style.grouping)")
        require(style.maxProjects == MenuBarMaxProjects.minimum,
                "the Compact style must become a one-slot budget, got \(style.maxProjects)")

        // 3. Idempotent, and a deliberate later choice survives.
        require(!migrate(fromBool), "the migration must not run twice")
        fromBool.set(MenuBarGrouping.project.rawValue, forKey: MenuBarGrouping.storageKey)
        require(!migrate(fromBool), "the migration must not re-fire after a deliberate choice")
        require(MenuBarAppearance.current(in: fromBool).grouping == .project,
                "a deliberate per-project choice must survive every later launch")

        // 4. A fresh install has nothing to carry and must have no key invented.
        let fresh = InMemoryDefaults()
        require(!migrate(fresh), "a fresh install has nothing to migrate")
        require(fresh.object(forKey: MenuBarGrouping.storageKey) == nil,
                "the migration must not invent a grouping key on a fresh install")
        require(fresh.object(forKey: MenuBarMaxProjects.storageKey) == nil,
                "the migration must not invent a slot-budget key on a fresh install")

        // 5. A user on any shipped style with compact OFF is untouched.
        for shipped in MenuBarStyle.allCases {
            let defaults = InMemoryDefaults()
            defaults.set(shipped.rawValue, forKey: MenuBarStyle.storageKey)
            require(!migrate(defaults), "\(shipped) is not a legacy value")
            require(defaults.string(forKey: MenuBarStyle.storageKey) == shipped.rawValue,
                    "\(shipped): the migration must not rewrite a shipped style")
            require(defaults.object(forKey: MenuBarGrouping.storageKey) == nil,
                    "\(shipped): the migration must not invent a grouping key")
        }
        return found
    }

    /// The real migration satisfies its whole contract.
    func testTheCompactMigrationSatisfiesItsContract() {
        XCTAssertEqual(
            Self.compactMigrationViolations(MenuBarAppearance.migrateLegacyCompactSetting(in:)),
            []
        )
    }

    /// Mutation-proved (committed): four ways to get this migration wrong,
    /// each an alternative implementation run against the same contract.
    ///
    /// AGENTS.md requires the mutation be committed as a fixture rather than
    /// only described. These are those fixtures: each must produce at least
    /// one violation, or the contract above is not checking what it claims.
    func testTheCompactMigrationMutantsAreAllRejected() {
        let mutants: [(String, StoreMigration)] = [
            ("no-op — the migration never runs", { _ in false }),
            ("carries the grouping but leaves the DEFAULT slot budget", { defaults in
                guard defaults.object(forKey: MenuBarGrouping.storageKey) == nil else { return false }
                let hadStyle = defaults.string(forKey: MenuBarStyle.storageKey)
                    == MenuBarAppearance.legacyCompactStyleRawValue
                let hadBool = defaults.bool(forKey: MenuBarAppearance.legacyCompactStorageKey)
                guard hadStyle || hadBool else { return false }
                if hadStyle {
                    defaults.set(MenuBarStyle.lights.rawValue, forKey: MenuBarStyle.storageKey)
                }
                defaults.set(MenuBarGrouping.combined.rawValue, forKey: MenuBarGrouping.storageKey)
                defaults.set(MenuBarMaxProjects.defaultValue, forKey: MenuBarMaxProjects.storageKey)
                return true
            }),
            ("carries the slot budget but the WRONG grouping", { defaults in
                guard defaults.object(forKey: MenuBarGrouping.storageKey) == nil else { return false }
                let hadStyle = defaults.string(forKey: MenuBarStyle.storageKey)
                    == MenuBarAppearance.legacyCompactStyleRawValue
                let hadBool = defaults.bool(forKey: MenuBarAppearance.legacyCompactStorageKey)
                guard hadStyle || hadBool else { return false }
                if hadStyle {
                    defaults.set(MenuBarStyle.lights.rawValue, forKey: MenuBarStyle.storageKey)
                }
                defaults.set(MenuBarGrouping.project.rawValue, forKey: MenuBarGrouping.storageKey)
                defaults.set(MenuBarMaxProjects.minimum, forKey: MenuBarMaxProjects.storageKey)
                return true
            }),
            ("unguarded — re-fires after the user chose per-project", { defaults in
                let hadStyle = defaults.string(forKey: MenuBarStyle.storageKey)
                    == MenuBarAppearance.legacyCompactStyleRawValue
                let hadBool = defaults.bool(forKey: MenuBarAppearance.legacyCompactStorageKey)
                guard hadStyle || hadBool else { return false }
                if hadStyle {
                    defaults.set(MenuBarStyle.lights.rawValue, forKey: MenuBarStyle.storageKey)
                }
                defaults.set(MenuBarGrouping.combined.rawValue, forKey: MenuBarGrouping.storageKey)
                defaults.set(MenuBarMaxProjects.minimum, forKey: MenuBarMaxProjects.storageKey)
                return true
            }),
        ]
        for (name, mutant) in mutants {
            XCTAssertFalse(Self.compactMigrationViolations(mutant).isEmpty,
                           "the mutant '\(name)' passed the migration contract unchallenged")
        }
    }

    /// LOCK on the migration's *rendering* promise, end to end: a user who had
    /// asked for a compact icon must keep an equivalent one.
    ///
    /// Drives the real migration over a real store and renders whatever
    /// appearance falls out, rather than hand-building the pair and asserting
    /// two numbers. An earlier version of this test did the latter, which
    /// meant it could not have detected a migration that wrote the wrong
    /// grouping or the wrong budget — the very thing it is named for.
    ///
    /// **The 2-session case is the one that matters, and it is new to #1955.**
    /// `renderGroup` sends a bucket of 3 sessions or fewer to
    /// `renderCompactGroup` (overlapping plain dots), while
    /// `buildAggregateStatusSVG` routes every session through
    /// `aggregateRender` (pie plus count). An implementation that made
    /// "combined" mean "one bucket through the normal group renderer" would
    /// therefore change what a migrated user sees at 2 sessions while leaving
    /// the 5-session row green — which is why both are here.
    func testMigratedCompactUsersKeepTheirRendering() throws {
        for sessionCount in [2, 5] {
            let defaults = InMemoryDefaults()
            defaults.set(true, forKey: MenuBarAppearance.legacyCompactStorageKey)
            XCTAssertTrue(MenuBarAppearance.migrateLegacyCompactSetting(in: defaults))

            let migrated = MenuBarAppearance.current(in: defaults)
            let sessions = MenuBarFixtures.acrossProjectsWithQuota(sessionCount)
            let aggregate = try XCTUnwrap(
                MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: sessions),
                "\(sessionCount) sessions: the aggregate path must render"
            )
            // Byte-identical, not merely the same width: at 2 sessions the two
            // candidate paths happen to differ in width too, but at some
            // session counts they would not, and the SVG is what ships.
            let dots = try XCTUnwrap(MenuBarStatusRenderer.buildStatusImage(
                sessions: sessions, projectGroupOrder: [], maxGroups: migrated.maxProjects
            ))
            XCTAssertNotEqual(
                dots.size.width, aggregate.width, accuracy: 0.01,
                "\(sessionCount) sessions: the two paths must differ, or this row proves nothing"
            )
            XCTAssertEqual(
                MenuBarImageBuilder.dotsImage(
                    appearance: migrated, sessions: sessions,
                    projectGroupOrder: [], daemonLabels: [:]
                )?.size.width ?? -1,
                aggregate.width, accuracy: 0.01,
                "\(sessionCount) sessions: the migrated appearance must draw the aggregate dot"
            )
            XCTAssertNil(
                MenuBarImageBuilder.quotaImage(
                    appearance: migrated, sessions: sessions, providerKeys: [], now: now
                ),
                "\(sessionCount) sessions: the migrated appearance must draw no quota half"
            )
            // And the whole icon, not just the halves.
            XCTAssertEqual(
                MenuBarImageBuilder.iconImage(
                    appearance: migrated, sessions: sessions,
                    projectGroupOrder: [], daemonLabels: [:], providerKeys: [], now: now
                )?.size.width ?? -1,
                aggregate.width, accuracy: 0.01,
                "\(sessionCount) sessions: the migrated icon must be the aggregate dot alone"
            )
        }
    }

    // MARK: - The ordered subscription slots (#1955 decision 4)

    /// The provider-list migration's contract, in the same committed-mutant
    /// shape as the compact migration above.
    static func providerMigrationViolations(_ migrate: StoreMigration) -> [String] {
        var found: [String] = []
        func require(_ condition: Bool, _ message: @autoclosure () -> String) {
            if !condition { found.append(message()) }
        }

        // 1. #909's single provider becomes a one-entry list, legacy key kept.
        let legacy = InMemoryDefaults()
        legacy.set("anthropic", forKey: MenuBarQuotaProvider.storageKey)
        require(migrate(legacy), "a stored single provider must be carried over")
        require(MenuBarQuotaProviders.current(in: legacy) == ["anthropic"],
                "got \(MenuBarQuotaProviders.current(in: legacy)) instead of [anthropic]")
        require(legacy.string(forKey: MenuBarQuotaProvider.storageKey) == "anthropic",
                "the legacy provider key must be left in place for a downgrade")

        // 2. A list the user already built must never be clobbered.
        let chosen = InMemoryDefaults()
        chosen.set("anthropic", forKey: MenuBarQuotaProvider.storageKey)
        chosen.set(MenuBarQuotaProviders.encode(["openai"]),
                   forKey: MenuBarQuotaProviders.storageKey)
        require(!migrate(chosen), "the migration must not fire once the new key exists")
        require(MenuBarQuotaProviders.current(in: chosen) == ["openai"],
                "got \(MenuBarQuotaProviders.current(in: chosen)) — a chosen list was clobbered")

        // 3. An EMPTY list is a choice (it means Auto), not an absent key.
        //    This is the row that separates "has the new key been written" from
        //    "is the list empty" — the latter re-adds the legacy provider on
        //    every launch after the user cleared it.
        let cleared = InMemoryDefaults()
        cleared.set("anthropic", forKey: MenuBarQuotaProvider.storageKey)
        cleared.set("", forKey: MenuBarQuotaProviders.storageKey)
        require(!migrate(cleared), "the migration must not fire against a deliberately empty list")
        require(MenuBarQuotaProviders.current(in: cleared).isEmpty,
                "a deliberate Auto must survive every later launch")

        // 4. Nothing to carry, nothing invented.
        let fresh = InMemoryDefaults()
        require(!migrate(fresh), "a fresh install has nothing to migrate")
        require(fresh.object(forKey: MenuBarQuotaProviders.storageKey) == nil,
                "the migration must not invent a providers key on a fresh install")

        // 5. Idempotent.
        require(!migrate(legacy), "the migration must not run twice")
        return found
    }

    func testTheProviderMigrationSatisfiesItsContract() {
        XCTAssertEqual(
            Self.providerMigrationViolations(
                MenuBarQuotaProviders.migrateLegacySingleProvider(in:)
            ),
            []
        )
    }

    /// Mutation-proved (committed): three ways to get the provider migration
    /// wrong, each run against the contract above.
    func testTheProviderMigrationMutantsAreAllRejected() {
        let mutants: [(String, StoreMigration)] = [
            ("no-op — the migration never runs", { _ in false }),
            ("unguarded — clobbers a list the user already built", { defaults in
                let legacy = defaults.string(forKey: MenuBarQuotaProvider.storageKey) ?? ""
                guard !legacy.isEmpty else { return false }
                defaults.set(MenuBarQuotaProviders.encode([legacy]),
                             forKey: MenuBarQuotaProviders.storageKey)
                return true
            }),
            ("guards on EMPTINESS — re-adds the provider after the user chose Auto", { defaults in
                guard MenuBarQuotaProviders.current(in: defaults).isEmpty else { return false }
                let legacy = defaults.string(forKey: MenuBarQuotaProvider.storageKey) ?? ""
                guard !legacy.isEmpty else { return false }
                defaults.set(MenuBarQuotaProviders.encode([legacy]),
                             forKey: MenuBarQuotaProviders.storageKey)
                return true
            }),
        ]
        for (name, mutant) in mutants {
            XCTAssertFalse(Self.providerMigrationViolations(mutant).isEmpty,
                           "the mutant '\(name)' passed the provider migration contract unchallenged")
        }
    }

    /// New for #1995: the migration itself carries no new logic (it moves a
    /// raw string between two keys, agnostic to what the string means), but
    /// #1995 changed what a stored key like "anthropic" is CHECKED AGAINST —
    /// `providerKey(adapter:)` now requires a confirmed `provider` field
    /// rather than inferring one from `planType`/`adapter`. This connects the
    /// two: a migrated key must still resolve a REAL, confirmed session's
    /// snapshot through `QuotaMenuBarRenderer.selectedSnapshot`, not just
    /// decode back out of `UserDefaults`.
    ///
    /// Mutation-proved: stamping the fixture session with
    /// `attributionQuality: nil` instead of `.confirmed` — simulating an
    /// unconfirmed identity — reddened this test (`selected` came back nil)
    /// while leaving `testTheProviderMigrationSatisfiesItsContract` green,
    /// since that contract never looks past `UserDefaults`. Reverted after
    /// confirming red; the permanent form of that same perturbation is
    /// committed below as
    /// `testAMigratedProviderKeyDoesNotSelectAnUnconfirmedSnapshot`.
    func testAMigratedProviderKeyStillSelectsAConfirmedSnapshot() throws {
        let defaults = InMemoryDefaults()
        defaults.set("anthropic", forKey: MenuBarQuotaProvider.storageKey)
        XCTAssertTrue(MenuBarQuotaProviders.migrateLegacySingleProvider(in: defaults),
                      "the legacy single provider must be carried over")
        let migratedKey = try XCTUnwrap(MenuBarQuotaProviders.current(in: defaults).first,
                                        "the migration produced no provider key")
        XCTAssertEqual(migratedKey, "anthropic")

        let confirmedSession = MenuBarFixtures.sessionWithQuota()
        let selected = QuotaMenuBarRenderer.selectedSnapshot(
            sessions: [confirmedSession], providerKey: migratedKey
        )
        XCTAssertEqual(selected?.windows.first?.usedPercent,
                       confirmedSession.metrics?.rateLimit?.windows.first?.usedPercent,
                       "the migrated key \"\(migratedKey)\" did not select the confirmed Anthropic "
                       + "session's own snapshot")
    }

    /// The must-not-match twin of the test above, committed as a permanent
    /// fixture rather than only described (AGENTS.md's testing-philosophy:
    /// "prefer committing that mutation to describing it") — this is the
    /// same perturbation the commit's mutation ran by hand against
    /// `MenuBarFixtures` (stamping `attributionQuality: nil` instead of
    /// `.confirmed`, observing red, then reverting), now encoded as its own
    /// permanent case so a future regression here is caught on every run
    /// rather than only once. Without the confirmed-identity requirement
    /// this session would still resolve under the migrated "anthropic" key
    /// (its `provider` field alone matches) — an unconfirmed identity must
    /// not select as if it were confirmed.
    func testAMigratedProviderKeyDoesNotSelectAnUnconfirmedSnapshot() throws {
        let defaults = InMemoryDefaults()
        defaults.set("anthropic", forKey: MenuBarQuotaProvider.storageKey)
        XCTAssertTrue(MenuBarQuotaProviders.migrateLegacySingleProvider(in: defaults))
        let migratedKey = try XCTUnwrap(MenuBarQuotaProviders.current(in: defaults).first)

        let unconfirmedRateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 20, windowMinutes: 300,
                                          resetsAt: now.addingTimeInterval(3600))],
            sampledAt: now,
            provider: "anthropic",
            attributionQuality: nil // the defect this test catches: unconfirmed
        )
        let unconfirmedSession = SessionState(
            id: "sess_unconfirmed", state: .working, model: "claude-sonnet", cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: now, updatedAt: now,
            metrics: SessionMetrics(
                elapsedSeconds: 0, totalTokens: 0, modelName: "claude-sonnet",
                contextWindow: nil, contextUtilization: 0, pressureLevel: "safe",
                contextWindowUnknown: nil, estimatedCostUSD: nil, lastAssistantText: nil,
                tasks: nil, rateLimit: unconfirmedRateLimit
            ),
            adapter: "claude-code"
        )
        XCTAssertNil(
            QuotaMenuBarRenderer.selectedSnapshot(sessions: [unconfirmedSession], providerKey: migratedKey),
            "a session with an unconfirmed provider must not be selected as though it were confirmed"
        )
    }

    /// The delimited-`String` encoding is a serializer, so AGENTS.md asks for a
    /// property test over generated input rather than only hand-written cases.
    ///
    /// The generated keys are the shapes `RateLimitInfo.providerKey(adapter:)`
    /// and `SettingsView.knownQuotaProviderKeys` actually produce — plan-type
    /// keys and `unknown:<adapter>` fallbacks — plus the punctuation an adapter
    /// name can carry.
    func testProviderKeysSurviveAnEncodeDecodeRoundTrip() {
        var generator = SystemRandomNumberGenerator()
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz0123456789-_.")
        func randomKey() -> String {
            let body = String((0..<Int.random(in: 1...12, using: &generator)).map { _ in
                alphabet.randomElement(using: &generator)!
            })
            return Bool.random(using: &generator) ? "unknown:\(body)" : body
        }

        // Unique lists, because that is the domain: the write path removes
        // before it appends, so a repeat is unreachable through the UI, and
        // `decode` deliberately collapses one (see its doc — SwiftUI's
        // `ForEach(id: \.self)` requires it). Generating repeats here would
        // assert the opposite of the shipped contract, at random.
        for _ in 0..<200 {
            var keys: [String] = []
            while keys.count < Int.random(in: 0...6, using: &generator) {
                let candidate = randomKey()
                if !keys.contains(candidate) { keys.append(candidate) }
            }
            XCTAssertEqual(
                MenuBarQuotaProviders.decode(MenuBarQuotaProviders.encode(keys)), keys,
                "encode/decode must be a round trip for \(keys)"
            )
        }

        // And mutations of the RAW string — the shapes a hand-edited plist or a
        // half-written value produces — must degrade to a clean list rather
        // than to a slot asking for the provider named "".
        let mutated: [(String, [String])] = [
            ("", []),
            (",", []),
            (",,,", []),
            ("anthropic,", ["anthropic"]),
            (",anthropic", ["anthropic"]),
            ("anthropic,,openai", ["anthropic", "openai"]),
            (" anthropic , openai ", ["anthropic", "openai"]),
            ("   ", []),
        ]
        for (raw, want) in mutated {
            XCTAssertEqual(MenuBarQuotaProviders.decode(raw), want,
                           "decoding the mutated raw value \"\(raw)\"")
        }
    }

    /// One quota slot per selected provider, drawn in list order — the
    /// rendering half of decision 4.
    ///
    /// Mutation-proved (source): drop the `for key in providerKeys` loop from
    /// `MenuBarImageBuilder.quotaImage` and return the single-provider render,
    /// and the two-slot row goes red.
    func testEachSelectedProviderGetsItsOwnQuotaSlot() throws {
        let anthropic = MenuBarFixtures.sessionWithQuota()
        // Different fills, deliberately: two slots rendered from identical
        // percentages produce identical pixels, and the ordering assertion
        // below would then pass whichever order the slots came out in.
        let openai = MenuBarFixtures.sessionWithQuota(
            adapter: "codex", fiveHourPercent: 85, sevenDayPercent: 5
        )
        let sessions = [anthropic, openai]
        let usage = appearance(.usage, .project)

        // The vacuity guard: both keys must actually resolve to a snapshot, or
        // the multi-slot row below would be "two nils composed to nothing".
        let anthropicKey = try XCTUnwrap(
            anthropic.metrics?.rateLimit?.providerKey(adapter: anthropic.adapter)
        )
        let openaiKey = try XCTUnwrap(
            openai.metrics?.rateLimit?.providerKey(adapter: openai.adapter)
        )
        XCTAssertNotEqual(anthropicKey, openaiKey,
                          "the fixture must carry two DIFFERENT providers")

        let one = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions, providerKeys: [anthropicKey], now: now
        ))
        let two = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions,
            providerKeys: [anthropicKey, openaiKey], now: now
        ))
        XCTAssertEqual(two.size.width,
                       one.size.width * 2 + MenuBarStatusRenderer.groupGap, accuracy: 0.01,
                       "two selected providers must compose two slots with one groupGap")

        // Order is the list's order, not the freshness order — checked in
        // pixels, because width is commutative and cannot see it.
        let reversed = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions,
            providerKeys: [openaiKey, anthropicKey], now: now
        ))
        XCTAssertEqual(reversed.size.width, two.size.width, accuracy: 0.01)
        XCTAssertNotEqual(two.tiffRepresentation, reversed.tiffRepresentation,
                          "reversing the provider list must reverse the slots")

        // An empty list is #909's behaviour: one slot, freshest wins.
        let auto = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions, providerKeys: [], now: now
        ))
        XCTAssertEqual(auto.size.width, one.size.width, accuracy: 0.01,
                       "an empty list must keep #909's single-slot Auto layout")

        // A selected provider with no renderable snapshot contributes no slot,
        // rather than a blank one the icon then carries forever.
        let missing = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions,
            providerKeys: [anthropicKey, "no-such-provider"], now: now
        ))
        XCTAssertEqual(missing.size.width, one.size.width, accuracy: 0.01,
                       "an unrenderable provider must not reserve a slot")

        // And when EVERY selected provider is unrenderable the half must be
        // nil, not a zero-width image. This is the arm that decides whether
        // `.usage` lies about the world: `showsDots` brings the dots back only
        // for a nil quota, so a zero-width placeholder here would leave a
        // `.usage` user staring at an empty icon while sessions run.
        XCTAssertNil(
            MenuBarImageBuilder.quotaImage(
                appearance: usage, sessions: sessions,
                providerKeys: ["no-such-provider", "nor-this-one"], now: now
            ),
            "an all-unrenderable selection must answer nil so the dots come back"
        )
        let fellBack = try XCTUnwrap(MenuBarImageBuilder.iconImage(
            appearance: usage, sessions: sessions, projectGroupOrder: [], daemonLabels: [:],
            providerKeys: ["no-such-provider"], now: now
        ))
        let dots = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: usage, sessions: sessions, projectGroupOrder: [], daemonLabels: [:]
        ))
        XCTAssertEqual(fellBack.size.width, dots.size.width, accuracy: 0.01,
                       "with no renderable slot, .usage must compose its dots")
    }

    // MARK: - The icon repaints when a menu-bar setting changes

    /// The change check's contract, as a pure check over a candidate snapshot
    /// reader — so the "field dropped from the snapshot" mutants can be RUN.
    ///
    /// `MenuBarController` rebuilds the icon when `MenuBarIconSettings`
    /// changes, and `UserDefaults.didChangeNotification` fires for every write
    /// — so a setting missing from that snapshot leaves the icon stale until
    /// something unrelated repaints it, with nothing failing.
    static func changeCheckViolations(
        _ snapshot: (UserDefaults) -> MenuBarIconSettings
    ) -> [String] {
        // (key, a value that differs from the unset default)
        let writes: [(String, Any)] = [
            (MenuBarStyle.storageKey, MenuBarStyle.combined.rawValue),
            (MenuBarGrouping.storageKey, MenuBarGrouping.combined.rawValue),
            (MenuBarMaxProjects.storageKey, MenuBarMaxProjects.maximum),
            (MenuBarQuotaProviders.storageKey, "anthropic"),
            (QuotaVisualStyle.storageKey, QuotaVisualStyle.circle.rawValue),
        ]
        var found: [String] = []
        for (key, value) in writes {
            let defaults = InMemoryDefaults()
            let before = snapshot(defaults)
            defaults.set(value, forKey: key)
            if snapshot(defaults) == before {
                found.append("writing \(key) is invisible to the icon's change check")
            }
        }
        // The counterpart: without it, a snapshot that simply reported
        // "changed" every time would satisfy every row above while
        // busy-rebuilding the icon on every write in the app.
        let unrelated = InMemoryDefaults()
        let before = snapshot(unrelated)
        unrelated.set(true, forKey: "showQuotaForecast")
        if snapshot(unrelated) != before {
            found.append("an unrelated default repaints the icon")
        }
        return found
    }

    func testEveryMenuBarSettingIsVisibleToTheChangeCheck() {
        XCTAssertEqual(Self.changeCheckViolations(MenuBarIconSettings.current(in:)), [])
    }

    /// Mutation-proved (committed): one mutant per field, each a snapshot
    /// reader that hard-codes that field out — which is the failure #1852
    /// built `MenuBarIconSettings` to make impossible and #1955 added three
    /// more chances to commit.
    func testTheChangeCheckMutantsAreAllRejected() {
        func rebuilt(
            _ defaults: UserDefaults,
            style: MenuBarStyle? = nil,
            grouping: MenuBarGrouping? = nil,
            maxProjects: Int? = nil,
            quotaProviders: String? = nil,
            quotaVisual: String? = nil
        ) -> MenuBarIconSettings {
            let real = MenuBarIconSettings.current(in: defaults)
            return MenuBarIconSettings(
                appearance: MenuBarAppearance(
                    style: style ?? real.appearance.style,
                    grouping: grouping ?? real.appearance.grouping,
                    maxProjects: maxProjects ?? real.appearance.maxProjects
                ),
                quotaProviders: quotaProviders ?? real.quotaProviders,
                quotaVisual: quotaVisual ?? real.quotaVisual
            )
        }
        let mutants: [(String, (UserDefaults) -> MenuBarIconSettings)] = [
            ("style pinned", { rebuilt($0, style: .lights) }),
            ("grouping pinned", { rebuilt($0, grouping: .project) }),
            ("slot budget pinned", { rebuilt($0, maxProjects: MenuBarMaxProjects.defaultValue) }),
            ("provider list pinned", { rebuilt($0, quotaProviders: "") }),
            ("quota visual pinned", { rebuilt($0, quotaVisual: "") }),
            ("always different", { _ in
                MenuBarIconSettings(
                    appearance: MenuBarAppearance(
                        style: .lights, grouping: .project,
                        maxProjects: Int.random(in: 1...MenuBarMaxProjects.maximum)
                    ),
                    quotaProviders: UUID().uuidString,
                    quotaVisual: UUID().uuidString
                )
            }),
        ]
        for (name, mutant) in mutants {
            XCTAssertFalse(Self.changeCheckViolations(mutant).isEmpty,
                           "the mutant '\(name)' passed the change-check contract unchallenged")
        }
    }

    // MARK: - The wiring, pinned by source

    /// `MenuBarController` is the only place a real `NSStatusItem` is created,
    /// and no test constructs one (it would allocate a live status item on
    /// whatever host runs the suite — the host dependency `docs/swift-testing.md`
    /// exists to remove). So the two lines that actually apply the identity
    /// are pinned by reading the source, in the idiom
    /// `PersistentDefaultsLintTests` / `RealHomePathLintTests` already use.
    ///
    /// Mutation-proved (source): delete either the `autosaveName` assignment or
    /// the `migrateLegacyPreferredPosition` call from `MenuBarController.swift`
    /// and this goes red.
    func testMenuBarControllerAppliesTheStatusItemIdentity() throws {
        let source = try Self.source(at: Self.menuBarControllerPath)

        XCTAssertTrue(
            source.contains("statusItem.autosaveName = MenuBarStatusItemIdentity.autosaveName"),
            "MenuBarController must declare the status item's autosave name"
        )
        XCTAssertTrue(
            source.contains("MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: UserDefaults.standard)"),
            "MenuBarController must carry a legacy position over before creating the item"
        )

        // Order is the whole point, and the deadline is the `autosaveName`
        // ASSIGNMENT, not the item's construction: an item with no declared
        // name still reads AppKit's generated key, so it is the assignment
        // that points AppKit at the new key and the value has to be there
        // first. Pinning against construction as well, since the two are one
        // line apart and the earlier bound is the safer one to hold.
        let migrate = try XCTUnwrap(source.range(of: "migrateLegacyPreferredPosition"))
        let create = try XCTUnwrap(source.range(of: "NSStatusBar.system.statusItem"))
        let assign = try XCTUnwrap(
            source.range(of: "autosaveName = MenuBarStatusItemIdentity.autosaveName")
        )
        XCTAssertTrue(migrate.lowerBound < assign.lowerBound,
                      "the migration must run BEFORE autosaveName is assigned")
        XCTAssertTrue(migrate.lowerBound < create.lowerBound,
                      "the migration must run BEFORE the status item is created")
    }

    /// Both preference migrations have their own deadline, and it is a
    /// different one: they must run before the controller snapshots
    /// `MenuBarIconSettings` as "last seen". Snapshotting first would capture
    /// the pre-migration values, so the very next unrelated defaults write
    /// would read as a change and repaint — harmless, but it means the
    /// ordering is load-bearing and nothing else would notice it being wrong.
    ///
    /// Pinned by source for the same reason as the identity wiring above: no
    /// test constructs a real `MenuBarController`.
    ///
    /// Mutation-proved (source): delete either migration call from
    /// `MenuBarController.swift`, or move it below the `lastIconSettings`
    /// assignment, and this goes red.
    func testMenuBarControllerRunsBothMigrationsBeforeSnapshotting() throws {
        let code = try Self.codeLines(at: Self.menuBarControllerPath)
        let calls = [
            "MenuBarAppearance.migrateLegacyCompactSetting(in: UserDefaults.standard)",
            "MenuBarQuotaProviders.migrateLegacySingleProvider(in: UserDefaults.standard)",
        ]
        let snapshot = try XCTUnwrap(
            code.range(of: "self.lastIconSettings = MenuBarIconSettings"),
            "MenuBarController must snapshot the icon settings at launch"
        )
        for call in calls {
            XCTAssertTrue(code.contains(call),
                          "MenuBarController must run \(call) at launch")
            let migrate = try XCTUnwrap(code.range(of: call))
            XCTAssertTrue(migrate.lowerBound < snapshot.lowerBound,
                          "\(call) must run BEFORE the settings snapshot is taken")
        }
    }

    /// The Settings gate that decides whether the quota sub-controls appear
    /// asks the appearance (`showsQuotaBars`) instead of testing `!= .lights`.
    /// `SettingsViewTests` renders the view but samples only corner-pixel
    /// opacity, and there is no Settings snapshot suite — so swapping
    /// `showsQuotaBars` for its neighbour `usesNarrowQuotaBars` would silently
    /// strip both sub-controls from every `.usage` user. Pinned by source, in
    /// the same idiom as the controller wiring above.
    ///
    /// Mutation-proved (source): change the property named at
    /// `SettingsView.swift`'s quota-section gate and this goes red.
    func testSettingsGatesTheQuotaSubPickersOnShowsQuotaBars() throws {
        let code = try Self.codeLines(at: Self.settingsViewPath)
        XCTAssertTrue(
            code.contains("if menuBarAppearance.showsQuotaBars {"),
            "the quota sub-controls must be gated on the appearance's own showsQuotaBars"
        )
        // And the property means what the gate needs it to mean: exactly the
        // two styles that shipped with those controls visible, at every
        // grouping — it is a content question, not a density one.
        for style in MenuBarStyle.allCases {
            for grouping in MenuBarGrouping.allCases {
                XCTAssertEqual(
                    appearance(style, grouping).showsQuotaBars,
                    style == .usage || style == .combined,
                    "\(style)/\(grouping) quota-control visibility"
                )
            }
        }
    }

    /// Restates `testSettingsOffersTheCompactModifierAsAToggle` against the
    /// controls that replaced that toggle. Nothing else can see this: the
    /// style picker is built from `MenuBarStyle.allCases`, so a missing
    /// grouping control leaves a perfectly valid three-segment picker and two
    /// settings the user can never reach.
    ///
    /// Mutation-proved (source): delete the grouping
    /// `EqualWidthSegmentedControl`, or the max-projects `Stepper`, from
    /// `SettingsView.swift` and this goes red.
    func testSettingsOffersTheGroupingAndTheSlotBudget() throws {
        let code = try Self.codeLines(at: Self.settingsViewPath)
        XCTAssertTrue(code.contains("selection: groupingSelection"),
                      "Settings must offer the grouping as its own control")
        XCTAssertTrue(
            code.contains("labels: MenuBarGrouping.selectableCases.map(\\.label)"),
            "and derive its segments from selectableCases, so nothing unimplemented is offered"
        )
        XCTAssertTrue(code.contains("value: maxProjectsSelection"),
                      "Settings must offer the slot budget as a stepper")
        XCTAssertTrue(
            code.contains("in: MenuBarMaxProjects.minimum...MenuBarMaxProjects.maximum"),
            "and bound that stepper by the type's own limits rather than by literals"
        )
        XCTAssertTrue(code.contains("@AppStorage(MenuBarGrouping.storageKey)"),
                      "the grouping must bind to its own storage key")
        XCTAssertTrue(code.contains("@AppStorage(MenuBarMaxProjects.storageKey)"),
                      "and so must the slot budget")
        XCTAssertTrue(code.contains("@AppStorage(MenuBarQuotaProviders.storageKey)"),
                      "and so must the ordered provider list")
        // #1955 phase 2: the budget row's NOUN follows the grouping, because
        // under `By location` the budget caps machines. Pinned as the derived
        // read rather than as any one string, so a fourth grouping cannot
        // reach the UI with a label somebody forgot to write.
        XCTAssertTrue(
            code.contains("Text(menuBarAppearance.grouping.resolved.slotBudgetLabel)"),
            "the slot-budget row must take its label from the grouping, not from a "
                + "literal that says 'projects' under every one of them"
        )
        XCTAssertTrue(code.contains("isOn: quotaProviderSelection(key)"),
                      "the provider list must be offered as a multi-select")
        // The styles keep their own control: three segments, not four.
        XCTAssertTrue(code.contains("labels: MenuBarStyle.allCases.map(\\.label)"),
                      "the style picker must stay derived from allCases")
        // And the removed Bool must not come back as a control.
        let raw = try Self.source(at: Self.settingsViewPath)
        XCTAssertFalse(raw.contains("$menuBarCompact"),
                       "#1955 removed the compact toggle — it must not be re-added beside "
                           + "the grouping it became a point in")
    }

    /// **This pin covers ROUTING only, and that limit is itself a finding.**
    ///
    /// Review of #1852 measured what a source-text pin cannot do. An earlier
    /// version of this test tried to cover `combinedImage`'s call sites by
    /// pinning their text — because `combinedImage` needs a live
    /// `SessionManager` and no test can call it — and four mutations walked
    /// straight through: `.init(…)` evaded a ban on the literal
    /// `MenuBarAppearance(`; a hand-off count taken over the whole file was
    /// satisfied by an unrelated fourth occurrence; and a ternary inversion
    /// and a same-typed argument swap were invisible to both. A `contains`
    /// check sees the call, never what the call is *given* or what is *done
    /// with* the result.
    ///
    /// The composition moved into `MenuBarImageBuilder.iconImage`, and the
    /// semantics now live in
    /// `MenuBarImageBuilderTests.testComposedIconForEveryStyleAndGrouping` and
    /// `…testComposedIconPutsTheDotsBeforeTheQuotaBars`, which call it and
    /// assert the composed image. What remains here is the cheap structural
    /// claim those cannot make: the decisions still live in the named seams
    /// rather than being re-derived inline.
    ///
    /// Mutation-proved (source): delete any of the four seam calls from
    /// `MenuBarImageBuilder.iconImage` / `combinedImage` and this goes red.
    func testImageBuilderRoutesBothHalvesThroughTheExtractedSeams() throws {
        let code = try Self.codeLines(at: Self.imageBuilderPath)
        XCTAssertTrue(code.contains("let computedDotsImage = dotsImage("),
                      "iconImage must route the dot half through dotsImage(appearance:...)")
        XCTAssertTrue(code.contains("let builtQuotaImage = quotaImage("),
                      "iconImage must route the quota half through quotaImage(appearance:...)")
        XCTAssertTrue(code.contains("let shownDotsImage = showsDots("),
                      "iconImage must route the dot-visibility decision through showsDots")
        XCTAssertTrue(code.contains("appearance: MenuBarAppearance.current"),
                      "combinedImage must read the appearance once, from one place")
        XCTAssertTrue(
            code.contains("providerKeys: MenuBarQuotaProviders.current(in: UserDefaults.standard)"),
            "combinedImage must read the ordered provider list once, from one place (#1955)"
        )
        // The slot budget travels ON the appearance rather than as a second
        // argument nobody threads — pinned because a `maxGroups:` that fell
        // back to the renderer's default would leave every width assertion in
        // this file green (they all sit at that default).
        XCTAssertTrue(code.contains("maxGroups: appearance.maxProjects"),
                      "dotsImage must hand the user's slot budget to the renderer")
        // #1955 phase 2: the daemon label map is the one thing `location`
        // needs that only the manager has, and `rememberedOrderViolations`'
        // `realIconRender` MIRRORS this call rather than being it. Pinned so
        // the mirror cannot silently stop matching — and pinned as
        // `sessionManager.daemonLabels`, the merged accessor, not as either
        // raw map, because reaching past it is how the icon's bucket names and
        // `SessionRowView`'s tooltips would start disagreeing.
        XCTAssertTrue(code.contains("daemonLabels: sessionManager.daemonLabels"),
                      "combinedImage must hand the merged daemon label map to iconImage")

        // Negative pins read the RAW source, never the comment-stripped form.
        // `codeLines` is a naive stripper — it also truncates a line at a `//`
        // inside a string literal — and for a "must NOT appear" assertion,
        // losing text can only turn a real hit into a silent PASS. A banned
        // construct sitting in a comment is a false positive somebody can see;
        // one hidden by an over-strip is a false negative nobody can. AGENTS.md:
        // a validator that cannot parse its input checks MORE, never less.
        //
        // Derived from `allCases` × both operators rather than hand-listing the
        // spellings that happened to exist before — the same reasoning the
        // enum's own doc gives for switching instead of comparing, applied to
        // the test that enforces it. Extended to `MenuBarGrouping` by #1955,
        // which added a second enum this file can be decided from.
        let raw = try Self.source(at: Self.imageBuilderPath)
        for style in MenuBarStyle.allCases {
            for op in ["==", "!="] {
                let comparison = "style \(op) .\(style.rawValue)"
                XCTAssertFalse(raw.contains(comparison),
                               "no style decision may go back to the non-exhaustive "
                                   + "`\(comparison)` — switch over MenuBarAppearance instead")
            }
        }
        for grouping in MenuBarGrouping.allCases {
            for op in ["==", "!="] {
                let comparison = "grouping \(op) .\(grouping.rawValue)"
                XCTAssertFalse(raw.contains(comparison),
                               "no grouping decision may go back to the non-exhaustive "
                                   + "`\(comparison)` — ask the appearance's own predicate")
            }
        }
    }

    /// The vacuity guard for `codeLines`, and the reason it exists.
    ///
    /// On `main` the seam pin above asserted `"let quotaImage = quotaImage("`
    /// — a string that appears in `MenuBarImageBuilder.swift` exactly once,
    /// **inside a comment** explaining why the local is NOT named that. The
    /// real call site is `let builtQuotaImage = quotaImage(`. So the
    /// assertion passed while proving nothing about the call site: deleting
    /// the routing entirely would have left it green as long as the
    /// explanatory comment stayed. (Verified on 183a7222 with
    /// `git show …:MenuBarImageBuilder.swift | grep -n`: one hit, at the
    /// comment.)
    ///
    /// `codeLines` strips `//` comments so a source pin cannot be satisfied by
    /// prose. Mutation-proved (source): make `stripComments` return its input
    /// unchanged and the comment-only assertion below goes red.
    ///
    /// The fixture is a literal rather than a real file. An earlier version
    /// asserted against the actual comment in `MenuBarImageBuilder.swift` that
    /// #1849's pin had matched, which made rewording an explanatory comment
    /// break an unrelated test — a loud failure, but a needless coupling.
    func testSourcePinsCannotBeSatisfiedByAComment() throws {
        // The shape of #1849's defect: the searched-for text present ONLY
        // inside a comment, with different real code on the same line.
        let fixture = """
            let builtQuotaImage = quotaImage(  // unlike let quotaImage = quotaImage(
            static func quotaImage(
            """
        let stripped = MenuBarAppearanceTests.strippedForTesting(fixture)

        XCTAssertTrue(fixture.contains("let quotaImage = quotaImage("),
                      "the fixture must contain the comment-only string, "
                          + "or this test exercises nothing")
        XCTAssertFalse(stripped.contains("let quotaImage = quotaImage("),
                       "a source pin must not be satisfiable by a string that only "
                           + "appears inside a comment")
        // And stripping must not have eaten the code: "could not look" and
        // "found nothing" must stay distinguishable.
        XCTAssertTrue(stripped.contains("let builtQuotaImage = quotaImage("),
                      "stripComments must keep the code before the comment")
        XCTAssertTrue(stripped.contains("static func quotaImage("),
                      "stripComments must keep comment-free lines intact")

        // The real file still parses to real code, so the positive pins above
        // are reading something.
        let code = try Self.codeLines(at: Self.imageBuilderPath)
        XCTAssertTrue(code.contains("static func quotaImage("),
                      "codeLines must still return the actual code")
    }

    /// A source file this suite cannot read is a FAILURE, never a quiet pass.
    /// Without the guard, a renamed or moved `MenuBarController.swift` would
    /// make every assertion above silently vacuous — "could not look" and
    /// "found nothing" must not produce the same result (AGENTS.md, Testing).
    /// This test proves the guard fires, and it now covers every path this
    /// suite reads rather than only the controller: #1955 renamed this file,
    /// which is exactly the moment a path constant goes stale unnoticed.
    func testTheSourceReaderFailsLoudlyWhenItCannotLook() {
        XCTAssertThrowsError(
            try Self.source(at: "platforms/macos/Irrlicht/App/NoSuchFile.swift"),
            "a source file that is not there must throw, not return empty"
        )
        for path in [Self.menuBarControllerPath, Self.imageBuilderPath, Self.settingsViewPath] {
            XCTAssertNoThrow(try Self.source(at: path),
                             "\(path) must still be readable, or its pins are vacuous")
        }
    }

    private static let menuBarControllerPath =
        "platforms/macos/Irrlicht/App/MenuBarController.swift"
    private static let imageBuilderPath =
        "platforms/macos/Irrlicht/App/MenuBarImageBuilder.swift"
    private static let settingsViewPath =
        "platforms/macos/Irrlicht/Views/SettingsView.swift"

    /// The source with `//` comment text removed. **For POSITIVE pins only.**
    ///
    /// A pin that greps the raw file can be satisfied by a comment that merely
    /// *mentions* the code it is looking for, which is not a hypothetical:
    /// #1849's pin for the quota seam matched only an explanatory comment and
    /// never the call site (see `testSourcePinsCannotBeSatisfiedByAComment`).
    ///
    /// Deliberately naive: it strips from the first `//` on a line to the end
    /// of that line, and knows nothing about `/* */` or about `//` inside a
    /// string literal. That last case is live rather than theoretical — in one
    /// of the three files pinned here, `SettingsView.swift`'s
    /// `"ws://localhost:7839"` placeholder is truncated at `= "ws:` — so the
    /// stripper really does eat code sometimes.
    ///
    /// Which is exactly why **negative pins must read `source(at:)` instead.**
    /// For a positive pin, over-stripping can only cause a spurious FAILURE,
    /// which somebody sees and fixes. For a `XCTAssertFalse(contains(…))` pin
    /// it would cause a silent PASS — the check quietly doing less, which is
    /// the direction AGENTS.md rules out: a validator that cannot parse its
    /// input checks MORE, never less.
    private static func codeLines(at relativePath: String) throws -> String {
        stripComments(from: try source(at: relativePath))
    }

    /// Test-visible alias, so `testSourcePinsCannotBeSatisfiedByAComment` can
    /// exercise the stripper against a literal.
    static func strippedForTesting(_ source: String) -> String { stripComments(from: source) }

    /// Split out from `codeLines` so the guard above can exercise it against a
    /// literal, without making a comment in shipped source load-bearing.
    private static func stripComments(from source: String) -> String {
        source
            .split(separator: "\n", omittingEmptySubsequences: false)
            .map { line -> Substring in
                guard let comment = line.range(of: "//") else { return line }
                return line[line.startIndex..<comment.lowerBound]
            }
            .joined(separator: "\n")
    }

    private static func source(at relativePath: String) throws -> String {
        let url = repoRoot().appendingPathComponent(relativePath)
        guard FileManager.default.fileExists(atPath: url.path) else {
            throw SourceUnreadable.missing(url.path)
        }
        return try String(contentsOf: url, encoding: .utf8)
    }

    /// Walk up from this source file to the repo root. `#filePath` is the
    /// checkout that compiled the test, so this works in a worktree too.
    private static func repoRoot() -> URL {
        URL(fileURLWithPath: #filePath)          // .../platforms/macos/Tests/<this file>
            .deletingLastPathComponent()          // .../platforms/macos/Tests
            .deletingLastPathComponent()          // .../platforms/macos
            .deletingLastPathComponent()          // .../platforms
            .deletingLastPathComponent()          // repo root
    }
}

private extension String {
    /// Right-align into a fixed-width column so the measured width table
    /// prints as a table rather than as ragged text.
    func padded(to width: Int) -> String {
        count >= width ? self : String(repeating: " ", count: width - count) + self
    }
}

/// Deliberately not `XCTSkip`: a source file this suite cannot read is a
/// failure, and naming it after the skip type would invite exactly the
/// silent pass the guard exists to prevent.
private enum SourceUnreadable: Error, CustomStringConvertible {
    case missing(String)

    var description: String {
        switch self {
        case .missing(let path): return "source not found at \(path)"
        }
    }
}
