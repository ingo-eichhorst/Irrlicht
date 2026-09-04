import XCTest
@testable import Irrlicht

/// Issue #1845 — the compact menu bar rendering and the status item's declared
/// autosave name — as reshaped by issue #1852, which turned Compact from a
/// fourth `MenuBarStyle` case into an orthogonal modifier on all three styles.
///
/// Nothing here is a regression test: both issues are enhancements, so there is
/// no defect that ran red on `main`. Every assertion is therefore one of two
/// kinds, and each one says which:
///
/// - **LOCK** — pins behavior that must NOT change. Passes on `main` by
///   construction wherever the symbol it names already exists there. The
///   load-bearing one for #1852 is
///   `testShippedStylesRenderExactlyWhatTheyDidBeforeWhenCompactIsOff`: an
///   existing install must render byte-identically until the user opts in.
/// - **Mutation-proved** — a check this change ADDS, which has no "before".
///   The doc comment names the exact source mutation that turns it red, per
///   AGENTS.md's Testing section.
///
/// The file keeps its name so the reshape reads as a diff rather than as a
/// delete-and-add. Two blocks in it are about neither compactness nor styles —
/// the `MenuBarStatusRenderer` geometry tests and the whole NSStatusItem
/// autosave/position-migration block — and would read better as their own
/// suites; that split is deliberately not bundled into #1852.
@MainActor
final class MenuBarCompactStyleTests: XCTestCase {

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

    // MARK: - The styles, and the modifier that is not one of them

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

    /// Compact is a modifier on every style, not a fourth style (#1852).
    ///
    /// Mutation-proved: re-add `case compact` to `MenuBarStyle` and the
    /// `allCases` assertion goes red; change `compactStorageKey` and the key
    /// assertion goes red.
    func testCompactIsAModifierAndNotAStyle() {
        XCTAssertEqual(MenuBarStyle.allCases.count, 3,
                       "compact is a modifier — it must not come back as a style")
        XCTAssertNil(MenuBarStyle(rawValue: MenuBarAppearance.legacyCompactStyleRawValue),
                     "\"compact\" must no longer parse as a style")
        XCTAssertEqual(MenuBarAppearance.compactStorageKey, "menuBarCompact")
        // Off unless asked for: an empty store must produce the pre-#1852
        // appearance, which is what the compatibility bound rests on.
        let fresh = MenuBarAppearance.current(in: InMemoryDefaults())
        XCTAssertEqual(fresh.style, .lights)
        XCTAssertFalse(fresh.isCompact, "the modifier must default to off")
    }

    // MARK: - What each appearance renders

    /// The full behaviour matrix — every style × the modifier, which is the
    /// table issue #1852 specifies.
    ///
    /// The `compact: false` half is a **LOCK**: those twelve values are what
    /// `MenuBarStyle`'s four predicates returned on `main`
    /// (`MenuBarStyle.swift:44/54/64/74` at #1849's merge), and they must not
    /// move. The `compact: true` half is new behaviour, mutation-proved:
    /// flip any arm of `MenuBarAppearance.usesNarrowQuotaBars` or
    /// `.aggregatesSessionDots` and the matching row goes red.
    func testAppearanceRenderingDecisions() {
        // (style, isCompact, showsQuotaBars, usesNarrowQuotaBars,
        //  aggregatesSessionDots, hidesDotsWhenQuotaIsRenderable)
        let expected: [(MenuBarStyle, Bool, Bool, Bool, Bool, Bool)] = [
            // ---- LOCK: byte-identical to what each style rendered before ----
            (.lights, false, false, false, false, false),
            (.usage, false, true, false, false, true),
            (.combined, false, true, true, false, false),
            // ---- new: the modifier turned on ----
            // lights + compact reproduces #1849's Compact style exactly.
            (.lights, true, false, false, true, false),
            // usage + compact reaches the narrow bars combined already used —
            // the combination #1845's fourth case could not express at all.
            (.usage, true, true, true, true, true),
            // combined + compact aggregates the dots and leaves the quota
            // half alone; it was already narrow.
            (.combined, true, true, true, true, false),
        ]
        XCTAssertEqual(expected.count, MenuBarStyle.allCases.count * 2,
                       "a style was added without both of its modifier rows here")
        for (style, compact, quota, narrow, aggregate, hidesDots) in expected {
            let appearance = MenuBarAppearance(style: style, isCompact: compact)
            XCTAssertEqual(appearance.showsQuotaBars, quota, "\(style) compact=\(compact) showsQuotaBars")
            XCTAssertEqual(appearance.usesNarrowQuotaBars, narrow,
                           "\(style) compact=\(compact) usesNarrowQuotaBars")
            XCTAssertEqual(appearance.aggregatesSessionDots, aggregate,
                           "\(style) compact=\(compact) aggregatesSessionDots")
            XCTAssertEqual(appearance.hidesDotsWhenQuotaIsRenderable, hidesDots,
                           "\(style) compact=\(compact) hidesDotsWhenQuotaIsRenderable")
        }
    }

    /// Two of the four predicates answer a `style` question only — the
    /// modifier must not move them. That is what keeps "compact" a question
    /// about DENSITY rather than a second way to choose content, which is the
    /// distinction #1852 exists to restore.
    ///
    /// Mutation-proved: make `showsQuotaBars` or
    /// `hidesDotsWhenQuotaIsRenderable` read `isCompact` and this goes red.
    func testTheModifierChangesDensityOnlyNeverContent() {
        for style in MenuBarStyle.allCases {
            let off = MenuBarAppearance(style: style, isCompact: false)
            let on = MenuBarAppearance(style: style, isCompact: true)
            XCTAssertEqual(off.showsQuotaBars, on.showsQuotaBars,
                           "\(style): the modifier must not change WHETHER quota bars are drawn")
            XCTAssertEqual(off.hidesDotsWhenQuotaIsRenderable, on.hidesDotsWhenQuotaIsRenderable,
                           "\(style): the modifier must not change whether dots yield to quota")
        }
    }

    /// Before #1852, `hidesDotsWhenQuotaIsRenderable` happened to equal
    /// `showsQuotaBars && !usesNarrowQuotaBars` across all four styles, and
    /// `testStyleRenderingDecisions` asserted that coincidence so that the day
    /// it stopped holding would be a deliberate, visible decision rather than
    /// a surprise. **This is that day**, and this test is the decision:
    /// `usage` + compact is the counterexample, because it is the first
    /// appearance that both hides its dots and draws narrow bars.
    ///
    /// Kept as an assertion rather than deleted so the derived form cannot
    /// quietly be reintroduced as a "simplification" of the two switches.
    func testTheDerivedEquivalenceNoLongerHolds() {
        let usageCompact = MenuBarAppearance(style: .usage, isCompact: true)
        XCTAssertTrue(usageCompact.hidesDotsWhenQuotaIsRenderable)
        XCTAssertTrue(usageCompact.usesNarrowQuotaBars)
        XCTAssertNotEqual(
            usageCompact.hidesDotsWhenQuotaIsRenderable,
            usageCompact.showsQuotaBars && !usageCompact.usesNarrowQuotaBars,
            "usage+compact must remain the counterexample that keeps "
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

    /// Mutation-proved: invert the condition in
    /// `MenuBarImageBuilder.dotsImage` and this goes red.
    ///
    /// Since #1852 the loop crosses the modifier as well as the style — the
    /// aggregate path is reachable from all three styles, not just the one
    /// that used to hard-code it.
    func testDotsImageRoutesTheModifierToTheAggregateRendererAndNothingElse() {
        let sessions = sessionsAcrossProjects(6)
        for style in MenuBarStyle.allCases {
            for compact in [false, true] {
                let appearance = MenuBarAppearance(style: style, isCompact: compact)
                let image = MenuBarImageBuilder.dotsImage(
                    appearance: appearance, sessions: sessions, projectGroupOrder: []
                )
                XCTAssertNotNil(image, "\(style) compact=\(compact) must still render dots")
                let width = image?.size.width ?? -1
                if appearance.aggregatesSessionDots {
                    XCTAssertEqual(width, 18.5, accuracy: 0.01,
                                   "\(style) compact=\(compact) must use the aggregate dot")
                } else {
                    XCTAssertEqual(width, 90.0, accuracy: 0.01,
                                   "\(style) compact=\(compact) must keep the per-project layout")
                }
            }
        }
    }

    /// Mutation-proved: swap `usesNarrowQuotaBars` for `showsQuotaBars` in
    /// the `compact:` argument of `MenuBarImageBuilder.quotaImage`, or invert
    /// its `showsQuotaBars` guard, and this goes red.
    ///
    /// The widths are read from `QuotaMenuBarRenderer`'s own constants rather
    /// than restated, so they cannot drift away from what it renders.
    func testQuotaImageRendersOnlyForTheStylesThatCarryItAndInTheRightLayout() {
        let sessions = [sessionWithQuota()]
        let full = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrow = QuotaMenuBarRenderer.barWidth * QuotaMenuBarRenderer.compactBarWidthFactor

        for style in MenuBarStyle.allCases {
            for compact in [false, true] {
                let appearance = MenuBarAppearance(style: style, isCompact: compact)
                let image = MenuBarImageBuilder.quotaImage(
                    appearance: appearance, sessions: sessions, providerKey: nil, now: now
                )
                guard appearance.showsQuotaBars else {
                    XCTAssertNil(image, "\(style) compact=\(compact) carries no quota bars")
                    continue
                }
                XCTAssertNotNil(image, "\(style) compact=\(compact) must render quota bars")
                let expected = appearance.usesNarrowQuotaBars ? narrow : full
                XCTAssertEqual(image?.size.width ?? -1, expected, accuracy: 0.01,
                               "\(style) compact=\(compact) quota layout")
            }
        }
    }

    /// **LOCK — the hard constraint of #1852, and the one criterion inherited
    /// from #1845 that is not negotiable.** An existing install renders
    /// byte-identically until the user turns the modifier on.
    ///
    /// This is a lock, not a defect test: it passes on `main` by construction
    /// for the shipped styles, and its whole job is to keep passing. What it
    /// pins is the *numbers*, in points, straight out of the two render
    /// seams — so any reshaping of how those numbers are selected has to
    /// arrive at the same pixels. Note in particular the `.combined` row:
    /// narrow quota bars with the modifier OFF is exactly the behaviour that
    /// would have been lost had `isCompact` been made to control bar
    /// narrowness on `combined` (see `MenuBarAppearance.usesNarrowQuotaBars`).
    func testShippedStylesRenderExactlyWhatTheyDidBeforeWhenCompactIsOff() {
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
            let appearance = MenuBarAppearance(style: style, isCompact: false)
            XCTAssertEqual(
                MenuBarImageBuilder.dotsImage(
                    appearance: appearance, sessions: sessions, projectGroupOrder: []
                )?.size.width ?? -1,
                dots ?? -1, accuracy: 0.01, "\(style) dots"
            )
            let quotaImage = MenuBarImageBuilder.quotaImage(
                appearance: appearance, sessions: sessions, providerKey: nil, now: now
            )
            if let quota {
                XCTAssertEqual(quotaImage?.size.width ?? -1, quota, accuracy: 0.01, "\(style) quota")
            } else {
                XCTAssertNil(quotaImage, "\(style) quota")
            }
        }
    }

    /// LOCK on the migration's *rendering* promise, end to end: a user who had
    /// picked #1849's Compact style must keep an equivalent icon.
    ///
    /// Drives the real migration over a real store and renders whatever
    /// appearance falls out, rather than hand-building `lights` + compact and
    /// asserting two numbers. An earlier version did the latter, which meant it
    /// could not have detected a migration that wrote the wrong style or the
    /// wrong flag — the very thing it is named for (#1852 review).
    func testMigratedCompactStyleUsersKeepTheirRendering() throws {
        let defaults = InMemoryDefaults()
        defaults.set(MenuBarAppearance.legacyCompactStyleRawValue,
                     forKey: MenuBarStyle.storageKey)
        XCTAssertTrue(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults))

        let migrated = MenuBarAppearance.current(in: defaults)
        let sessions = MenuBarFixtures.acrossProjectsWithQuota(7)
        // 18.50 / nil are what `.compact` produced at #1849's merge.
        XCTAssertEqual(
            MenuBarImageBuilder.dotsImage(
                appearance: migrated, sessions: sessions, projectGroupOrder: []
            )?.size.width ?? -1,
            18.5, accuracy: 0.01,
            "the migrated appearance must still draw the constant-width aggregate dot"
        )
        XCTAssertNil(
            MenuBarImageBuilder.quotaImage(
                appearance: migrated, sessions: sessions, providerKey: nil, now: now
            ),
            "the migrated appearance must still draw no quota half"
        )
        // And the whole icon, not just the halves.
        XCTAssertEqual(
            MenuBarImageBuilder.iconImage(
                appearance: migrated, sessions: sessions,
                projectGroupOrder: [], providerKey: nil, now: now
            )?.size.width ?? -1,
            18.5, accuracy: 0.01,
            "the migrated icon must be the aggregate dot alone"
        )
    }

    // MARK: - Width (acceptance criterion 2)

    /// The measurement the issue asks for. Not an assertion — it prints the
    /// rendered width of every style × modifier combination so the figure in
    /// the PR body has a command behind it rather than being typed by hand.
    ///
    ///     cd platforms/macos && swift test --filter testMeasuredWidthOfEachStyle
    ///
    /// #1852 extends #1849's four columns to all six combinations rather than
    /// adding a second measurement, so one command still produces every
    /// number the issue quotes.
    ///
    /// Each cell is **derived from the appearance's own predicates**, not
    /// hand-composed per column the way #1849's four were. That is what stops
    /// the printed table and the shipped behaviour drifting apart: change
    /// `usesNarrowQuotaBars` and the table moves with it, instead of the
    /// arithmetic here quietly continuing to describe the old rules.
    func testMeasuredWidthOfEachStyle() throws {
        // Read from QuotaMenuBarRenderer rather than restated, so the trailing
        // note cannot drift away from what it actually renders.
        let fullQuota = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrowQuota = QuotaMenuBarRenderer.barWidth
            * QuotaMenuBarRenderer.compactBarWidthFactor

        let columns: [(String, MenuBarAppearance)] = MenuBarStyle.allCases.flatMap { style in
            [false, true].map { compact in
                ("\(style.label)\(compact ? "+C" : "")",
                 MenuBarAppearance(style: style, isCompact: compact))
            }
        }
        XCTAssertEqual(columns.count, MenuBarStyle.allCases.count * 2,
                       "every style × modifier combination must be measured")

        print("=== #1852 measured menu bar icon widths, in points ===")
        print("projects | " + columns.map { $0.0.padded(to: 8) }.joined(separator: " | "))
        for projects in [1, 2, 3, 5, 6, 8] {
            // Exactly one project carries a renderable quota, so the styles
            // that draw quota bars actually have one to draw. The earlier
            // fixture here carried NO rate-limit data at all, which meant the
            // Usage and Combined columns described a scenario the fixture did
            // not contain (#1852 review).
            let sessions = MenuBarFixtures.acrossProjectsWithQuota(projects)
            let cells = try columns.map { column -> String in
                // XCTUnwrap, not `?? 0`: an icon that regressed to nil would
                // otherwise print 0.00 and pass, making "could not measure"
                // and "measured zero" the same output — the exact failure this
                // file guards against for its source reader.
                let icon = try XCTUnwrap(
                    MenuBarImageBuilder.iconImage(
                        appearance: column.1, sessions: sessions,
                        projectGroupOrder: [], providerKey: nil, now: now
                    ),
                    "\(column.0) at \(projects) projects rendered no icon"
                )
                return String(format: "%.2f", icon.size.width).padded(to: 8)
            }
            print(String(format: "%8d | ", projects) + cells.joined(separator: " | "))
        }
        print(String(format: "=== measured from MenuBarImageBuilder.iconImage — the same "
                     + "composition the app ships; quota halves are %.2f full / %.2f narrow "
                     + "per QuotaMenuBarRenderer's constants; +C = compact modifier on ===",
                     fullQuota, narrowQuota))
    }

    /// Mutation-proved: make `buildAggregateStatusSVG` group by project (drop
    /// the single `aggregateRender` and call `orderedProjectGroups`) and this
    /// goes red — the width would start tracking the project count again.
    ///
    /// This is the property the issue is actually asking for: not "narrower"
    /// in one sampled configuration, but *constant* as projects accumulate,
    /// which is what stops the icon sliding behind the notch.
    func testCompactWidthDependsOnlyOnTheSessionCountsDigits() throws {
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
                       "Compact width must not vary with the project count, got \(oneDigit)")

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
        XCTAssertEqual(twoDigits[0] - oneDigit[0], 6.5, accuracy: 0.01,
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

    /// Mutation-proved: same mutation as above. Numeric, in the idiom
    /// `QuotaMenuBarRendererTests.testBuildSVGCompactIsNarrowerThanDefault`
    /// uses — anchored to a stated derivation, not a bare magic number.
    func testCompactIsNarrowerThanLightsOnACrowdedMenuBar() {
        // Six one-session projects: the case the issue describes.
        let sessions = sessionsAcrossProjects(6)
        let lights = MenuBarStatusRenderer.buildStatusSVG(
            sessions: sessions, projectGroupOrder: []
        )
        let compact = MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: sessions)
        XCTAssertNotNil(lights)
        XCTAssertNotNil(compact)
        XCTAssertLessThan(compact!.width, lights!.width)

        // Compact's width is dot + padding + one digit per digit of the count:
        // radius*2 + 2 + digits*6.5 = 10 + 2 + 6.5 = 18.5 for a 1-digit count.
        XCTAssertEqual(compact!.width, 18.5, accuracy: 0.01)
        // Lights at six one-session groups: 5 visible groups of one dot
        // (1*(10-4)+4 = 10 each) + an overflow marker (10) + 5 gaps of 6
        // = 50 + 10 + 30 = 90.
        XCTAssertEqual(lights!.width, 90.0, accuracy: 0.01)
    }

    /// Mutation-proved: drop the `topLevelSessions` filter from
    /// `buildAggregateStatusSVG` and this goes red — a subagent would be
    /// counted in the aggregate dot's number even though the per-project
    /// renderer has always excluded it.
    func testCompactCountsTopLevelSessionsOnly() {
        let sessions = [
            makeSession(id: "p", project: "a"),
            makeSession(id: "c", project: "a", parentSessionId: "p"),
        ]
        let built = MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: sessions)
        XCTAssertNotNil(built)
        XCTAssertTrue(built!.svg.contains(">1<"),
                      "the aggregate label must count only the top-level session, got: \(built!.svg)")
    }

    /// Mutation-proved: return a non-nil render for an empty list and this
    /// goes red. An empty aggregate must collapse to "no icon", the same way
    /// `buildStatusSVG`'s `totalWidth > 0` guard does, rather than drawing a
    /// bare "0".
    func testCompactRendersNothingWithoutSessions() {
        XCTAssertNil(MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: []))
        XCTAssertNil(MenuBarStatusRenderer.buildAggregateStatusSVG(sessions: [
            makeSession(id: "c", project: "a", parentSessionId: "p"),
        ]), "a lone subagent is not a drawable session")
    }

    /// LOCK: the per-project renderer is byte-identical after the shared
    /// `assemble`/`aggregateRender` extraction. This is the hard constraint —
    /// an existing install must render exactly as it did before.
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
        // which is this PR's hard constraint — so any geometry change
        // SHOULD fail here and be re-approved on purpose. The state colors
        // are read from `SessionState.State`, so a palette change does not
        // make it brittle.
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
    }

    /// Mutation-proved: in `MenuBarStatusRenderer.image(from:)`, delete
    /// `image.isTemplate = false` or change the size it stamps, and this goes
    /// red. The extraction gave that helper a second caller, which is the
    /// moment to pin it: a declared size that disagrees with the SVG canvas
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

    // MARK: - Status item autosave name (acceptance criterion 1)

    /// Mutation-proved: change the literal in
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

    /// Mutation-proved: delete the `defaults.set(...)` line in
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

    /// Mutation-proved: drop the `defaults.object(forKey: currentKey) == nil`
    /// guard and this goes red — a user who dragged the icon under the new
    /// name would have that position clobbered by a stale legacy value on
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

    /// Mutation-proved: drop the `let legacyValue = ...` guard and this goes
    /// red. A fresh install has nothing to migrate and must not have a key
    /// invented for it.
    func testMigrationIsANoOpOnAFreshInstall() {
        let defaults = InMemoryDefaults()
        let newKey = MenuBarStatusItemIdentity.preferredPositionKey(
            forAutosaveName: MenuBarStatusItemIdentity.autosaveName
        )
        XCTAssertFalse(MenuBarStatusItemIdentity.migrateLegacyPreferredPosition(in: defaults))
        XCTAssertNil(defaults.object(forKey: newKey))
    }

    // MARK: - Migrating off #1849's Compact style (acceptance criterion 4)

    /// Mutation-proved: delete either `defaults.set(...)` line in
    /// `MenuBarAppearance.migrateLegacyCompactStyle` and this goes red.
    ///
    /// Why it matters: `"compact"` does not parse as a style in this build, so
    /// `MenuBarStyle.current(in:)` falls back to `.lights` with the modifier OFF.
    /// Without the migration, the one group of users who had explicitly asked
    /// for a narrow icon would silently get the widest per-project layout back.
    func testLegacyCompactStyleIsCarriedOverToTheModifier() {
        let defaults = InMemoryDefaults()
        defaults.set(MenuBarAppearance.legacyCompactStyleRawValue,
                     forKey: MenuBarStyle.storageKey)

        XCTAssertTrue(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults),
                      "a persisted Compact style must be carried over")

        let migrated = MenuBarAppearance.current(in: defaults)
        XCTAssertEqual(migrated.style, .lights,
                       "Compact showed dots and no quota bars — that is Lights")
        XCTAssertTrue(migrated.isCompact, "and the modifier must be turned on")
        // The stored value is the parseable one, not left as "compact".
        XCTAssertEqual(defaults.string(forKey: MenuBarStyle.storageKey),
                       MenuBarStyle.lights.rawValue)
        XCTAssertTrue(defaults.bool(forKey: MenuBarAppearance.compactStorageKey))
    }

    /// Mutation-proved: drop the `guard` in `migrateLegacyCompactStyle` and
    /// this goes red — the migration would then force the modifier on for
    /// every user on every launch, overwriting a deliberate "off".
    func testCompactStyleMigrationIsIdempotentAndLeavesOtherChoicesAlone() {
        let defaults = InMemoryDefaults()
        defaults.set(MenuBarAppearance.legacyCompactStyleRawValue,
                     forKey: MenuBarStyle.storageKey)
        XCTAssertTrue(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults))

        // Second launch: nothing left to carry over.
        XCTAssertFalse(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults),
                       "the migration must not run twice")

        // And a user who then turns the modifier back off keeps it off.
        defaults.set(false, forKey: MenuBarAppearance.compactStorageKey)
        XCTAssertFalse(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults))
        XCTAssertFalse(defaults.bool(forKey: MenuBarAppearance.compactStorageKey),
                       "a deliberate off must survive every later launch")
    }

    /// Mutation-proved: drop the `guard` and this goes red for every row —
    /// a fresh install and a user on any shipped style must be untouched,
    /// which is the same compatibility bound the render lock states.
    func testCompactStyleMigrationIsANoOpForEveryoneElse() {
        // Fresh install: no keys invented.
        let fresh = InMemoryDefaults()
        XCTAssertFalse(MenuBarAppearance.migrateLegacyCompactStyle(in: fresh))
        XCTAssertNil(fresh.object(forKey: MenuBarStyle.storageKey))
        XCTAssertNil(fresh.object(forKey: MenuBarAppearance.compactStorageKey))

        // A user on any shipped style keeps it, with the modifier untouched.
        for style in MenuBarStyle.allCases {
            let defaults = InMemoryDefaults()
            defaults.set(style.rawValue, forKey: MenuBarStyle.storageKey)
            XCTAssertFalse(MenuBarAppearance.migrateLegacyCompactStyle(in: defaults),
                           "\(style) is not a legacy value")
            XCTAssertEqual(defaults.string(forKey: MenuBarStyle.storageKey), style.rawValue)
            XCTAssertNil(defaults.object(forKey: MenuBarAppearance.compactStorageKey),
                         "\(style): the migration must not invent a modifier key")
        }
    }

    // MARK: - The icon repaints when a menu-bar setting changes

    /// `MenuBarController` rebuilds the icon when `MenuBarIconSettings`
    /// changes, and `UserDefaults.didChangeNotification` fires for every write
    /// — so a setting missing from that snapshot leaves the icon stale until
    /// something unrelated repaints it, with nothing failing. Before #1852 the
    /// comparison was three hand-listed fields; the compact modifier was the
    /// fourth setting, and this is what makes forgetting it impossible.
    ///
    /// Mutation-proved: drop any field from `MenuBarIconSettings.current` (or
    /// hard-code `isCompact: false` there) and the matching row goes red.
    func testEveryMenuBarSettingIsVisibleToTheChangeCheck() {
        // (key, a value that differs from the unset default)
        let mutations: [(String, Any)] = [
            (MenuBarStyle.storageKey, MenuBarStyle.combined.rawValue),
            (MenuBarAppearance.compactStorageKey, true),
            (MenuBarQuotaProvider.storageKey, "anthropic"),
            (QuotaVisualStyle.storageKey, QuotaVisualStyle.circle.rawValue),
        ]
        for (key, value) in mutations {
            let defaults = InMemoryDefaults()
            let before = MenuBarIconSettings.current(in: defaults)
            defaults.set(value, forKey: key)
            let after = MenuBarIconSettings.current(in: defaults)
            XCTAssertNotEqual(before, after,
                              "writing \(key) must be visible to the icon's change check")
        }
    }

    /// The counterpart: an unrelated default must NOT repaint the icon.
    /// Without it the test above would still pass against a snapshot that
    /// simply reported "changed" every time, which would busy-rebuild the
    /// icon on every write in the app.
    func testAnUnrelatedDefaultDoesNotRepaintTheIcon() {
        let defaults = InMemoryDefaults()
        let before = MenuBarIconSettings.current(in: defaults)
        defaults.set(true, forKey: "showQuotaForecast")
        XCTAssertEqual(before, MenuBarIconSettings.current(in: defaults),
                       "only menu-bar icon settings may trigger a repaint")
    }

    // MARK: - The wiring, pinned by source

    /// `MenuBarController` is the only place a real `NSStatusItem` is created,
    /// and no test constructs one (it would allocate a live status item on
    /// whatever host runs the suite — the host dependency `docs/swift-testing.md`
    /// exists to remove). So the two lines that actually apply the identity
    /// are pinned by reading the source, in the idiom
    /// `PersistentDefaultsLintTests` / `RealHomePathLintTests` already use.
    ///
    /// Mutation-proved: delete either the `autosaveName` assignment or the
    /// `migrateLegacyPreferredPosition` call from `MenuBarController.swift`
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

    /// The compact-style migration has its own deadline, and it is a different
    /// one: it must run before the controller snapshots `MenuBarIconSettings`
    /// as "last seen". Snapshotting first would capture the pre-migration
    /// style, so the very next unrelated defaults write would read as a change
    /// and repaint — harmless, but it means the ordering is load-bearing and
    /// nothing else would notice it being wrong.
    ///
    /// Pinned by source for the same reason as the identity wiring above: no
    /// test constructs a real `MenuBarController`.
    ///
    /// Mutation-proved: delete the `migrateLegacyCompactStyle` call from
    /// `MenuBarController.swift`, or move it below the `lastIconSettings`
    /// assignment, and this goes red.
    func testMenuBarControllerMigratesTheLegacyCompactStyleBeforeSnapshotting() throws {
        let code = try Self.codeLines(at: Self.menuBarControllerPath)
        XCTAssertTrue(
            code.contains("MenuBarAppearance.migrateLegacyCompactStyle(in: UserDefaults.standard)"),
            "MenuBarController must carry a legacy Compact style over at launch"
        )
        let migrate = try XCTUnwrap(code.range(of: "migrateLegacyCompactStyle"))
        let snapshot = try XCTUnwrap(code.range(of: "self.lastIconSettings = MenuBarIconSettings"))
        XCTAssertTrue(migrate.lowerBound < snapshot.lowerBound,
                      "the migration must run BEFORE the settings snapshot is taken")
    }

    /// The Settings gate that decides whether the quota sub-pickers appear
    /// asks the appearance (`showsQuotaBars`) instead of testing `!= .lights`.
    /// `SettingsViewTests` renders the view but samples only corner-pixel
    /// opacity, and there is no Settings snapshot suite — so swapping
    /// `showsQuotaBars` for its neighbour `usesNarrowQuotaBars` would silently
    /// strip both sub-pickers from every `.usage` user. Pinned by source, in
    /// the same idiom as the controller wiring above.
    ///
    /// Mutation-proved: change the property named at `SettingsView.swift`'s
    /// quota-section gate and this goes red.
    func testSettingsGatesTheQuotaSubPickersOnShowsQuotaBars() throws {
        let code = try Self.codeLines(at: Self.settingsViewPath)
        XCTAssertTrue(
            code.contains("if menuBarAppearance.showsQuotaBars {"),
            "the quota sub-pickers must be gated on the appearance's own showsQuotaBars"
        )
        // And the property means what the gate needs it to mean: exactly the
        // two styles that shipped with those pickers visible, on both settings
        // of the modifier — it is a content question, not a density one.
        for style in MenuBarStyle.allCases {
            for compact in [false, true] {
                XCTAssertEqual(
                    MenuBarAppearance(style: style, isCompact: compact).showsQuotaBars,
                    style == .usage || style == .combined,
                    "\(style) compact=\(compact) quota-picker visibility"
                )
            }
        }
    }

    /// The Settings surface actually offers the modifier, and offers it as one
    /// control rather than as a fourth segment. Nothing else can see this: the
    /// segmented control is built from `MenuBarStyle.allCases`, so a missing
    /// toggle leaves a perfectly valid three-segment picker and a setting the
    /// user can never reach.
    ///
    /// Mutation-proved: delete the `LeadingToggle` for `$menuBarCompact` from
    /// `SettingsView.swift` and this goes red.
    func testSettingsOffersTheCompactModifierAsAToggle() throws {
        let code = try Self.codeLines(at: Self.settingsViewPath)
        XCTAssertTrue(code.contains("isOn: $menuBarCompact"),
                      "Settings must offer the compact modifier as its own toggle")
        XCTAssertTrue(
            code.contains("@AppStorage(MenuBarAppearance.compactStorageKey)"),
            "and bind it to the modifier's own storage key"
        )
        // The styles keep their own control: three segments, not four.
        XCTAssertTrue(code.contains("labels: MenuBarStyle.allCases.map(\\.label)"),
                      "the style picker must stay derived from allCases")
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
    /// `MenuBarImageBuilderTests.testComposedIconForEveryStyleAndModifier` and
    /// `…testComposedIconPutsTheDotsBeforeTheQuotaBars`, which call it and
    /// assert the composed image. What remains here is the cheap structural
    /// claim those cannot make: the decisions still live in the named seams
    /// rather than being re-derived inline.
    ///
    /// Mutation-proved: delete any of the three seam calls from
    /// `MenuBarImageBuilder.iconImage` and this goes red.
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

        // Negative pins read the RAW source, never the comment-stripped form.
        // `codeLines` is a naive stripper — it also truncates a line at a `//`
        // inside a string literal — and for a "must NOT appear" assertion,
        // losing text can only turn a real hit into a silent PASS. A banned
        // construct sitting in a comment is a false positive somebody can see;
        // one hidden by an over-strip is a false negative nobody can. AGENTS.md:
        // a validator that cannot parse its input checks MORE, never less.
        //
        // Derived from `allCases` × both operators rather than hand-listing the
        // three spellings that happened to exist before #1845 — the same
        // reasoning the enum's own doc gives for switching instead of
        // comparing, applied to the test that enforces it.
        let raw = try Self.source(at: Self.imageBuilderPath)
        for style in MenuBarStyle.allCases {
            for op in ["==", "!="] {
                let comparison = "style \(op) .\(style.rawValue)"
                XCTAssertFalse(raw.contains(comparison),
                               "no style decision may go back to the non-exhaustive "
                                   + "`\(comparison)` — switch over MenuBarAppearance instead")
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
    /// prose. Mutation-proved: make `stripComments` return its input unchanged
    /// and the comment-only assertion below goes red.
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
        let stripped = MenuBarCompactStyleTests.strippedForTesting(fixture)

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
    /// `testTheSourceReaderFailsLoudlyWhenItCannotLook` proves the guard fires.
    func testTheSourceReaderFailsLoudlyWhenItCannotLook() {
        XCTAssertThrowsError(
            try Self.source(at: "platforms/macos/Irrlicht/App/NoSuchFile.swift"),
            "a source file that is not there must throw, not return empty"
        )
        XCTAssertNoThrow(try Self.source(at: Self.menuBarControllerPath),
                         "and the real path must still be readable")
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
