import XCTest
@testable import Irrlicht

/// Issue #1845 — the Compact menu bar style and the status item's declared
/// autosave name.
///
/// Nothing here is a regression test: #1845 is an enhancement, so there is no
/// defect that ran red on `main`. Every assertion is therefore one of two
/// kinds, and each one says which:
///
/// - **LOCK** — pins behavior that must NOT change. Passes on `main` by
///   construction wherever the symbol it names already exists there.
/// - **Mutation-proved** — a check this change ADDS, which has no "before".
///   The doc comment names the exact source mutation that turns it red, per
///   AGENTS.md's Testing section.
@MainActor
final class MenuBarCompactStyleTests: XCTestCase {

    // MARK: - Fixtures

    private func makeSession(
        id: String,
        state: SessionState.State = .working,
        project: String,
        parentSessionId: String? = nil
    ) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/\(project)",
            projectName: project,
            firstSeen: Date(),
            updatedAt: Date(),
            parentSessionId: parentSessionId
        )
    }

    /// `count` sessions spread one-per-project, which is the layout that makes
    /// the per-project renderer widest for a given session count.
    private func sessionsAcrossProjects(_ count: Int) -> [SessionState] {
        (0..<count).map { makeSession(id: "s\($0)", project: "p\($0)") }
    }

    private let now = Date(timeIntervalSince1970: 1_700_000_000)

    /// A session carrying a renderable rate-limit snapshot, so the quota half
    /// of the icon has something to draw. Same shape
    /// `QuotaMenuBarRendererTests` uses.
    private func sessionWithQuota() -> SessionState {
        let metrics = SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: 0,
            modelName: "claude-sonnet",
            contextWindow: nil,
            contextUtilization: 0,
            pressureLevel: "safe",
            contextWindowUnknown: nil,
            estimatedCostUSD: nil,
            lastAssistantText: nil,
            tasks: nil,
            rateLimit: RateLimitInfo(
                windows: [
                    RateLimitWindowInfo(
                        usedPercent: 20, windowMinutes: 300,
                        resetsAt: now.addingTimeInterval(3600)
                    ),
                    RateLimitWindowInfo(
                        usedPercent: 40, windowMinutes: 10080,
                        resetsAt: now.addingTimeInterval(3 * 86400)
                    ),
                ],
                sampledAt: now
            )
        )
        return SessionState(
            id: "sess_quota",
            state: .working,
            model: "claude-sonnet",
            cwd: "/Users/test/projects/quota",
            projectName: "quota",
            firstSeen: now,
            updatedAt: now,
            metrics: metrics,
            adapter: "claude-code"
        )
    }

    // MARK: - The style exists and is opt-in

    /// LOCK on the compatibility rule: the three shipped styles keep their
    /// raw values and their order, and the default stays `.lights`. A user
    /// who never opens Settings must render exactly as before.
    func testExistingStylesAndDefaultAreUnchanged() {
        XCTAssertEqual(MenuBarStyle.allCases.prefix(3).map(\.rawValue), ["lights", "usage", "combined"])
        XCTAssertEqual(MenuBarStyle(rawValue: "lights"), .lights)
        XCTAssertEqual(MenuBarStyle(rawValue: "usage"), .usage)
        XCTAssertEqual(MenuBarStyle(rawValue: "combined"), .combined)
        // An unset or unknown value still falls back to .lights, so an
        // install that never opts in is untouched — and so is one that
        // downgrades after selecting a style this build does not know.
        XCTAssertEqual(MenuBarStyle(rawValue: "") ?? .lights, .lights)
        XCTAssertEqual(MenuBarStyle(rawValue: "no-such-style") ?? .lights, .lights)
    }

    /// Mutation-proved: delete `case compact` (and its `label` arm) and this
    /// fails to compile — the strongest red available for a new enum case.
    func testCompactStyleIsAvailableAndLast() {
        XCTAssertEqual(MenuBarStyle.compact.rawValue, "compact")
        XCTAssertEqual(MenuBarStyle.compact.label, "Compact")
        XCTAssertEqual(MenuBarStyle.allCases.last, .compact,
                       "Compact must stay last so the existing segments keep their positions")
    }

    // MARK: - What each style renders

    /// LOCK on all three shipped styles, plus the new case's answers.
    ///
    /// Mutation-proved for `.compact`: flip any `.compact` arm in
    /// `MenuBarStyle.showsQuotaBars` / `.usesNarrowQuotaBars` /
    /// `.aggregatesSessionDots` and the matching row goes red.
    func testStyleRenderingDecisions() {
        // (style, showsQuotaBars, usesNarrowQuotaBars, aggregatesSessionDots,
        //  hidesDotsWhenQuotaIsRenderable)
        let expected: [(MenuBarStyle, Bool, Bool, Bool, Bool)] = [
            (.lights, false, false, false, false),
            (.usage, true, false, false, true),
            (.combined, true, true, false, false),
            (.compact, false, false, true, false),
        ]
        XCTAssertEqual(expected.count, MenuBarStyle.allCases.count,
                       "a style was added without an expectation row here")
        for (style, quota, narrow, aggregate, hidesDots) in expected {
            XCTAssertEqual(style.showsQuotaBars, quota, "\(style).showsQuotaBars")
            XCTAssertEqual(style.usesNarrowQuotaBars, narrow, "\(style).usesNarrowQuotaBars")
            XCTAssertEqual(style.aggregatesSessionDots, aggregate, "\(style).aggregatesSessionDots")
            XCTAssertEqual(style.hidesDotsWhenQuotaIsRenderable, hidesDots,
                           "\(style).hidesDotsWhenQuotaIsRenderable")
        }

        // Across today's four styles `hidesDotsWhenQuotaIsRenderable` happens
        // to equal `showsQuotaBars && !usesNarrowQuotaBars`. It is kept as its
        // own exhaustive switch anyway, for the reason the enum's own comment
        // gives: a DERIVED answer would silently decide for a fifth style that
        // nobody thought about, which is the exact failure the switches
        // replaced. Asserting the coincidence here means the day it stops
        // holding is a deliberate, visible decision rather than a surprise.
        for style in MenuBarStyle.allCases {
            XCTAssertEqual(
                style.hidesDotsWhenQuotaIsRenderable,
                style.showsQuotaBars && !style.usesNarrowQuotaBars,
                "\(style): the derived-equivalence note above no longer holds — "
                    + "decide deliberately whether that is intended"
            )
        }
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
    func testDotsImageRoutesCompactToTheAggregateRendererAndNothingElse() {
        let sessions = sessionsAcrossProjects(6)
        for style in MenuBarStyle.allCases {
            let image = MenuBarImageBuilder.dotsImage(
                style: style, sessions: sessions, projectGroupOrder: []
            )
            XCTAssertNotNil(image, "\(style) must still render dots")
            let width = image?.size.width ?? -1
            if style.aggregatesSessionDots {
                XCTAssertEqual(width, 18.5, accuracy: 0.01,
                               "\(style) must use the constant-width aggregate dot")
            } else {
                XCTAssertEqual(width, 90.0, accuracy: 0.01,
                               "\(style) must keep the per-project layout it shipped with")
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
            let image = MenuBarImageBuilder.quotaImage(
                style: style, sessions: sessions, providerKey: nil, now: now
            )
            guard style.showsQuotaBars else {
                XCTAssertNil(image, "\(style) carries no quota bars")
                continue
            }
            XCTAssertNotNil(image, "\(style) must render quota bars")
            let expected = style.usesNarrowQuotaBars ? narrow : full
            XCTAssertEqual(image?.size.width ?? -1, expected, accuracy: 0.01,
                           "\(style) quota layout")
        }
    }

    /// LOCK on the hard constraint, stated as one table: what each SHIPPED
    /// style renders must be exactly what it rendered before `.compact`
    /// existed. `.lights` dots-only, `.usage` full-width bars, `.combined`
    /// dots plus narrow bars.
    func testShippedStylesRenderExactlyWhatTheyDidBefore() {
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
        for (style, dots, quota) in expectations {
            XCTAssertEqual(
                MenuBarImageBuilder.dotsImage(
                    style: style, sessions: sessions, projectGroupOrder: []
                )?.size.width ?? -1,
                dots ?? -1, accuracy: 0.01, "\(style) dots"
            )
            let quotaImage = MenuBarImageBuilder.quotaImage(
                style: style, sessions: sessions, providerKey: nil, now: now
            )
            if let quota {
                XCTAssertEqual(quotaImage?.size.width ?? -1, quota, accuracy: 0.01, "\(style) quota")
            } else {
                XCTAssertNil(quotaImage, "\(style) quota")
            }
        }
    }

    // MARK: - Width (acceptance criterion 2)

    /// The measurement the issue asks for. Not an assertion — it prints the
    /// rendered width of every style so the figure in the PR body has a
    /// command behind it rather than being typed by hand.
    ///
    ///     cd platforms/macos && swift test --filter testMeasuredWidthOfEachStyle
    func testMeasuredWidthOfEachStyle() throws {
        // Read from QuotaMenuBarRenderer rather than restated, so these
        // cannot drift away from what it actually renders.
        let fullQuota = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrowQuota = QuotaMenuBarRenderer.barWidth
            * QuotaMenuBarRenderer.compactBarWidthFactor
        let gap = MenuBarStatusRenderer.groupGap

        print("=== #1845 measured menu bar icon widths, in points ===")
        print("projects | Lights | Usage | Combined | Compact")
        for projects in [1, 2, 3, 5, 6, 8] {
            let sessions = sessionsAcrossProjects(projects)
            // XCTUnwrap, not `?? 0`: a renderer that regressed to nil would
            // otherwise print 0.00 and pass, making "could not measure" and
            // "measured zero" the same output — the exact failure this file
            // guards against for its source reader.
            let dots = try XCTUnwrap(MenuBarStatusRenderer.buildStatusSVG(
                sessions: sessions, projectGroupOrder: []
            )).width
            let aggregate = try XCTUnwrap(MenuBarStatusRenderer.buildAggregateStatusSVG(
                sessions: sessions
            )).width
            // Usage hides the dots unless the quota is unrenderable; Combined
            // shows dots + gap + narrow quota; Compact is dots only.
            print(String(
                format: "%8d | %6.2f | %5.2f | %8.2f | %7.2f",
                projects, dots, fullQuota, dots + gap + narrowQuota, aggregate
            ))
        }
        print(String(format: "=== dot half measured from the renderers; quota half "
                     + "from QuotaMenuBarRenderer's constants (full %.2f, narrow %.2f) ===",
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

    /// The Settings gate that decides whether the quota sub-pickers appear
    /// now asks the style (`showsQuotaBars`) instead of testing `!= .lights`.
    /// `SettingsViewTests` renders the view but samples only corner-pixel
    /// opacity, and there is no Settings snapshot suite — so swapping
    /// `showsQuotaBars` for its neighbour `usesNarrowQuotaBars` would silently
    /// strip both sub-pickers from every `.usage` user. Pinned by source, in
    /// the same idiom as the controller wiring above.
    ///
    /// Mutation-proved: change the property named at `SettingsView.swift`'s
    /// quota-section gate and this goes red.
    func testSettingsGatesTheQuotaSubPickersOnShowsQuotaBars() throws {
        let source = try Self.source(at: "platforms/macos/Irrlicht/Views/SettingsView.swift")
        XCTAssertTrue(
            source.contains("if (MenuBarStyle(rawValue: menuBarStyle) ?? .lights).showsQuotaBars {"),
            "the quota sub-pickers must be gated on the style's own showsQuotaBars"
        )
        // And the property means what the gate needs it to mean: exactly the
        // two styles that shipped with those pickers visible.
        for style in MenuBarStyle.allCases {
            XCTAssertEqual(style.showsQuotaBars, style == .usage || style == .combined,
                           "\(style) quota-picker visibility")
        }
    }

    /// Mutation-proved: invert or swap either condition in
    /// `MenuBarImageBuilder`'s two extracted seams and this goes red. Pins
    /// that `combinedImage` actually ROUTES through them rather than
    /// re-deciding inline — the behavioral tests above cannot see a copy of
    /// the logic left behind at the call site.
    func testImageBuilderRoutesBothHalvesThroughTheExtractedSeams() throws {
        let source = try Self.source(at: "platforms/macos/Irrlicht/App/MenuBarImageBuilder.swift")
        XCTAssertTrue(source.contains("let computedDotsImage = dotsImage("),
                      "combinedImage must route the dot half through dotsImage(style:...)")
        XCTAssertTrue(source.contains("let quotaImage = quotaImage("),
                      "combinedImage must route the quota half through quotaImage(style:...)")
        XCTAssertFalse(source.contains("style == .lights"),
                       "no style decision may go back to a non-exhaustive comparison")
        XCTAssertFalse(source.contains("style == .combined"),
                       "no style decision may go back to a non-exhaustive comparison")
        XCTAssertFalse(source.contains("style != .usage"),
                       "no style decision may go back to a non-exhaustive comparison")
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
