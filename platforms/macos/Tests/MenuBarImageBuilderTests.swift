import XCTest
@testable import Irrlicht

/// Menu-bar attention indicator for pending permission items: while any
/// consent item is unanswered the icon is fully replaced by the orange
/// attention flame — dots/off return once everything is answered.
@MainActor
final class MenuBarImageBuilderTests: XCTestCase {
    func testIconStatePicksAttentionWhenConsentPending() {
        // Attention outranks dots — even with live sessions.
        XCTAssertEqual(
            MenuBarImageBuilder.iconState(pendingConsentCount: 1, sessionCount: 3),
            .attention
        )
    }

    func testIconStateReturnsDotsWhenNoPendingConsent() {
        XCTAssertEqual(
            MenuBarImageBuilder.iconState(pendingConsentCount: 0, sessionCount: 2),
            .dots
        )
    }

    func testIconStateReturnsOffWhenIdle() {
        XCTAssertEqual(
            MenuBarImageBuilder.iconState(pendingConsentCount: 0, sessionCount: 0),
            .off
        )
    }

    func testAttentionSVGUsesOrangeBodyAndExclamationBadge() {
        let svg = OffFlameImage.buildSVG(pointSize: 18, config: .attention)
        // Orange flame body — not the gray no-sessions stops.
        XCTAssertTrue(svg.contains("#FFB347"), "attention body should be brand orange")
        XCTAssertFalse(svg.contains("#9ca3af"), "attention must not reuse the gray no-sessions stops")
        // Red badge with the white exclamation (stem + dot).
        XCTAssertTrue(svg.contains("#FF3B30"), "badge should be red")
        XCTAssertTrue(svg.contains("stroke-linecap=\"round\""), "exclamation stem should be present")
        XCTAssertTrue(svg.contains("<circle cx=\"990\" cy=\"1125\""), "exclamation dot should be present")
    }

    func testAttentionImageCarriesAccessibilityDescription() {
        XCTAssertEqual(
            OffFlameImage.attention.accessibilityDescription,
            "Irrlicht — action required: permission pending"
        )
    }

    // MARK: - composeSideBySide (issue #909: dots + quota composition)

    func testComposeSideBySideReturnsNilWhenBothNil() {
        XCTAssertNil(MenuBarImageBuilder.composeSideBySide(nil, nil))
    }

    func testComposeSideBySideReturnsLeftUnchangedWhenRightNil() {
        let left = NSImage(size: NSSize(width: 10, height: 18))
        let result = MenuBarImageBuilder.composeSideBySide(left, nil)
        XCTAssertEqual(result?.size, NSSize(width: 10, height: 18))
    }

    func testComposeSideBySideReturnsRightUnchangedWhenLeftNil() {
        let right = NSImage(size: NSSize(width: 12, height: 18))
        let result = MenuBarImageBuilder.composeSideBySide(nil, right)
        XCTAssertEqual(result?.size, NSSize(width: 12, height: 18))
    }

    func testComposeSideBySideSumsWidthAndTakesTallerHeightWhenBothPresent() {
        let left = NSImage(size: NSSize(width: 10, height: 18))
        let right = NSImage(size: NSSize(width: 20, height: 12))
        let result = MenuBarImageBuilder.composeSideBySide(left, right, gap: 4)
        XCTAssertEqual(result?.size.width, 34) // 10 + 4 + 20
        XCTAssertEqual(result?.size.height, 18) // max(18, 12)
    }

    // MARK: - shouldShowDotsInUsageStyle (issue #909 review fix)

    /// The appearance these tests exercise. `isCompact` defaults to false so
    /// every assertion below keeps meaning exactly what it meant before #1852
    /// made the modifier a separate axis — the LOCKs stay locks.
    private func appearance(
        _ style: MenuBarStyle, compact: Bool = false
    ) -> MenuBarAppearance {
        MenuBarAppearance(style: style, isCompact: compact)
    }

    /// `.usage` style with a renderable dots image but no quota yet must
    /// not collapse to the "no sessions" icon — see the comment in
    /// `combinedImage`. LOCK: the one arm #1862 kept — without it a
    /// `.usage` user with active sessions and no renderable quota yet would
    /// fall through to the "no sessions running" icon while sessions are in
    /// fact running.
    func testFallsBackToDotsWhenUsageStyleHasNoQuotaButDotsRendered() {
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertTrue(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.usage), quotaImage: nil, dotsImage: dots
        ))
    }

    /// LOCK: with quota renderable, `.usage` means quota only — including
    /// while a session is errored (#1862 removed the arm that used to make
    /// an exception here).
    func testDoesNotFallBackWhenUsageStyleHasQuotaImage() {
        let quota = NSImage(size: NSSize(width: 10, height: 18))
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.usage), quotaImage: quota, dotsImage: dots
        ))
    }

    /// Sessions can be active (non-zero count) while `buildStatusImage` still
    /// returns nil (e.g. every session is a subagent whose parent was pruned
    /// out from under it) — the fallback must key off the actual dots image,
    /// not a raw session count that doesn't guarantee dots render.
    func testDoesNotFallBackWhenUsageStyleHasNoRenderableDotsEither() {
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.usage), quotaImage: nil, dotsImage: nil
        ))
    }

    func testDoesNotFallBackForLightsOrCombinedStyles() {
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.lights), quotaImage: nil, dotsImage: dots
        ))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.combined), quotaImage: nil, dotsImage: dots
        ))
    }

    // MARK: - #1862: `.usage` honors its style strictly, even while errored

    /// #1802 added an arm to `shouldShowDotsInUsageStyle` so an errored
    /// session would bring the dots back on `.usage`. #1862 removed it:
    /// `iconImage` re-adds the WHOLE dot bank (`computedDotsImage`), not
    /// just the errored project, and an `error` state does not clear on a
    /// timer — so the escalation had become the normal view for every
    /// `.usage` user, not a rare alert. `.usage` now means quota bars only,
    /// error or not.
    ///
    /// This is the reporter's exact repro from #1862: a top-level session
    /// in `.error` state, sitting alongside a renderable quota. Exercised
    /// end-to-end through `iconImage` (rather than the removed
    /// `hasErroredSession` parameter, which no longer exists) so the
    /// assertion reflects what a real session list produces.
    func testUsageStyleKeepsDotsHiddenWhenASessionIsErrored() throws {
        let sessions = [
            MenuBarFixtures.session(id: "e1", state: .error, project: "alpha"),
            MenuBarFixtures.sessionWithQuota(),
        ]
        let usage = MenuBarAppearance(style: .usage, isCompact: false)
        let quota = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: usage, sessions: sessions, providerKey: nil, now: fixedNow
        ), "the fixture must have a renderable quota, or this test proves nothing")
        let composed = try XCTUnwrap(icon(usage, sessions: sessions))
        XCTAssertEqual(composed.size.width, quota.size.width, accuracy: 0.01,
                       "an errored top-level session must not widen .usage past its quota-only width")
    }

    /// #1862's guarantee has to hold at BOTH densities, and the LOCK above
    /// pins only `isCompact: false`.
    ///
    /// Compact is a modifier on HOW DENSELY the icon draws, never on WHAT it
    /// draws — that separation is the whole point of #1852. An error arm
    /// re-added under an `!isCompact` guard would therefore be a content
    /// decision wearing a density costume, and it would slip past the
    /// single-density LOCK above without anything going red. Crossing the
    /// modifier here closes that.
    ///
    /// Two errored projects rather than one, so the aggregate (`compact`) and
    /// per-project (`!compact`) dot layouts differ in width from each other —
    /// a fixture where both layouts happened to be the same width would pass
    /// this test for the wrong reason.
    ///
    /// Mutation-proved: replacing `shouldShowDotsInUsageStyle`'s
    /// `return quotaImage == nil` with `return true` — which is #1862's bug,
    /// re-expressed — turns this red at both densities while
    /// `testUsageStyleStillShowsDotsWhenErroredAndNoQuotaIsRenderable` below
    /// stays green.
    func testUsageStyleKeepsDotsHiddenWhenErroredAtBothDensities() throws {
        let sessions = [
            MenuBarFixtures.session(id: "e1", state: .error, project: "alpha"),
            MenuBarFixtures.session(id: "e2", state: .error, project: "beta"),
            MenuBarFixtures.sessionWithQuota(),
        ]
        for compact in [false, true] {
            let usage = MenuBarAppearance(style: .usage, isCompact: compact)
            let quota = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
                appearance: usage, sessions: sessions, providerKey: nil, now: fixedNow
            ), "compact=\(compact): the fixture must render a quota, or this proves nothing")
            let composed = try XCTUnwrap(icon(usage, sessions: sessions))
            XCTAssertEqual(
                composed.size.width, quota.size.width, accuracy: 0.01,
                "compact=\(compact): errored sessions must not widen .usage past quota-only"
            )
        }
    }

    /// LOCK on the arm #1862 KEPT, in the one combination where a later
    /// change is most likely to drop it by accident: a session is errored AND
    /// no quota is renderable.
    ///
    /// "`.usage` never shows dots" is the wrong summary of #1862, and it is
    /// the summary someone reads off the test above. Applied here it would
    /// collapse the icon to `OffFlameImage.menuBar` — the "no sessions
    /// running" icon — while sessions are in fact running and one of them has
    /// failed, which is a worse lie about the world than the widening #1862
    /// removed.
    ///
    /// Mutation-proved: replacing that same `return quotaImage == nil` with
    /// `return false` turns this red while the two width LOCKs above stay
    /// green.
    func testUsageStyleStillShowsDotsWhenErroredAndNoQuotaIsRenderable() throws {
        // acrossProjects carries no rate-limit data, so the quota half cannot
        // render — see its doc, which names this as its intended use.
        var sessions = MenuBarFixtures.acrossProjects(2)
        sessions.append(MenuBarFixtures.session(id: "e1", state: .error, project: "gamma"))
        let usage = MenuBarAppearance(style: .usage, isCompact: false)
        XCTAssertNil(
            MenuBarImageBuilder.quotaImage(
                appearance: usage, sessions: sessions, providerKey: nil, now: fixedNow
            ),
            "this fixture must render NO quota, or the fallback under test never runs"
        )
        let dots = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: usage, sessions: sessions, projectGroupOrder: []
        ))
        let composed = try XCTUnwrap(
            icon(usage, sessions: sessions),
            "an errored session with no renderable quota must not collapse the icon"
        )
        XCTAssertEqual(composed.size.width, dots.size.width, accuracy: 0.01,
                       "with no quota to show, .usage falls back to its dots")
    }

    /// LOCK: without a renderable dots image, `.usage` can never show dots —
    /// the guard short-circuits before `quotaImage` is even inspected.
    func testNoDotsImageMeansNoDotsRegardlessOfQuota() {
        let quota = NSImage(size: NSSize(width: 20, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            appearance: appearance(.usage), quotaImage: quota, dotsImage: nil
        ))
    }

    /// The WHOLE dot-visibility decision, across every style × the modifier.
    ///
    /// `combinedImage` used to spell this inline as
    /// `!appearance.hidesDotsWhenQuotaIsRenderable || shouldShowDotsInUsageStyle(…)`,
    /// where nothing could reach it: `combinedImage` is private and needs a
    /// live `SessionManager`. Measured during #1852's mutation battery —
    /// swapping that first predicate for its neighbour `aggregatesSessionDots`
    /// left all 543 tests green while making `.usage` draw its dots
    /// unconditionally. `showsDots` is the extraction that closes it.
    ///
    /// Mutation-proved: replace `hidesDotsWhenQuotaIsRenderable` in
    /// `MenuBarImageBuilder.showsDots` with any other predicate and this goes
    /// red.
    func testShowsDotsAcrossEveryStyleAndDensity() {
        let quota = NSImage(size: NSSize(width: 20, height: 18))
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        for style in MenuBarStyle.allCases {
            for compact in [false, true] {
                // Only `.usage` ever withholds its dots, and only while the
                // quota is renderable. Density never enters into it.
                let expected = style != .usage
                XCTAssertEqual(
                    MenuBarImageBuilder.showsDots(
                        appearance: appearance(style, compact: compact),
                        quotaImage: quota, dotsImage: dots
                    ),
                    expected,
                    "\(style) compact=\(compact) with a renderable quota"
                )
                // With no quota to show, every style draws its dots.
                XCTAssertTrue(
                    MenuBarImageBuilder.showsDots(
                        appearance: appearance(style, compact: compact),
                        quotaImage: nil, dotsImage: dots
                    ),
                    "\(style) compact=\(compact) with no renderable quota"
                )
            }
        }
    }

    // MARK: - The composed icon (#1852 review)

    // Fixtures come from MenuBarFixtures, shared with MenuBarCompactStyleTests:
    // both suites assert the same derived widths, so a private second copy is
    // how they would quietly stop testing the same thing (see that file).

    private let fixedNow = MenuBarFixtures.now

    private func icon(_ appearance: MenuBarAppearance, sessions: [SessionState]) -> NSImage? {
        MenuBarImageBuilder.iconImage(
            appearance: appearance, sessions: sessions, projectGroupOrder: [],
            providerKey: nil, now: fixedNow
        )
    }

    /// The WHOLE composed icon, for every style × the modifier.
    ///
    /// This is the test the #1852 review asked for. Four mutations inside the
    /// old inline composition survived the entire suite, because every existing
    /// test called the extracted seams DIRECTLY and nothing exercised what the
    /// seams were handed or what was done with their results. Asserting the
    /// composed width closes three of the four; the ordering assertion below
    /// closes the fourth.
    ///
    /// The `compact: false` column is a **LOCK** — those three widths are what
    /// each style composed before #1852 existed.
    ///
    /// Mutation-proved: invert `? computedDotsImage : nil`, or swap the
    /// `quotaImage:`/`dotsImage:` arguments to `showsDots`, in
    /// `MenuBarImageBuilder.iconImage` — either turns this red.
    func testComposedIconForEveryStyleAndModifier() throws {
        let sessions = MenuBarFixtures.acrossProjectsWithQuota(7)
        let full = QuotaMenuBarRenderer.labelWidth + QuotaMenuBarRenderer.gap
            + QuotaMenuBarRenderer.barWidth
        let narrow = QuotaMenuBarRenderer.barWidth * QuotaMenuBarRenderer.compactBarWidthFactor
        let gap = MenuBarStatusRenderer.groupGap
        // 7 projects (6 + the quota carrier) exceeds maxVisibleGroups, so the
        // per-project half is at its 90.00 plateau; the aggregate is constant.
        let dots: CGFloat = 90.0
        let aggregate: CGFloat = 18.5

        // (style, isCompact, expected composed width)
        let expected: [(MenuBarStyle, Bool, CGFloat)] = [
            (.lights, false, dots),                    // LOCK: dots only
            (.usage, false, full),                     // LOCK: quota only, dots hidden
            (.combined, false, dots + gap + narrow),   // LOCK: dots + narrow quota
            (.lights, true, aggregate),
            (.usage, true, narrow),
            (.combined, true, aggregate + gap + narrow),
        ]
        XCTAssertEqual(expected.count, MenuBarStyle.allCases.count * 2,
                       "every style × modifier combination must be composed here")
        for (style, compact, width) in expected {
            let composed = try XCTUnwrap(
                icon(MenuBarAppearance(style: style, isCompact: compact), sessions: sessions),
                "\(style) compact=\(compact) must compose an icon"
            )
            XCTAssertEqual(composed.size.width, width, accuracy: 0.01,
                           "\(style) compact=\(compact) composed width")
        }
    }

    /// `.usage` with sessions running but NO renderable quota composes to the
    /// dots, never to nothing (#909's fallback, at both densities).
    ///
    /// **This is the case that discriminates the two `NSImage?` arguments of
    /// `showsDots`.** With both halves present, swapping `quotaImage:` and
    /// `dotsImage:` at the call site changes nothing — both are non-nil, so
    /// both spellings answer the same. Only when exactly one is nil do they
    /// diverge, which is why `testComposedIconForEveryStyleAndModifier` (whose
    /// fixture always carries quota) could not see it: that swap survived the
    /// full suite during #1852's review and again after the first fix.
    ///
    /// Mutation-proved: swap the `quotaImage:`/`dotsImage:` arguments to
    /// `showsDots` in `MenuBarImageBuilder.iconImage` and this goes red —
    /// `.usage` collapses to the "no sessions" icon while sessions run.
    func testUsageComposesDotsWhenNoQuotaIsRenderable() throws {
        // No rate-limit data anywhere, so the quota half cannot render.
        let sessions = MenuBarFixtures.acrossProjects(6)
        for compact in [false, true] {
            let appearance = MenuBarAppearance(style: .usage, isCompact: compact)
            let quota = MenuBarImageBuilder.quotaImage(
                appearance: appearance, sessions: sessions, providerKey: nil, now: fixedNow
            )
            XCTAssertNil(quota, "the fixture must have no renderable quota, "
                             + "or this test proves nothing about the fallback")
            let composed = try XCTUnwrap(
                icon(appearance, sessions: sessions),
                "usage compact=\(compact) must fall back to the dots, not to nothing"
            )
            XCTAssertEqual(composed.size.width, compact ? 18.5 : 90.0, accuracy: 0.01,
                           "usage compact=\(compact) fallback must be the dot half alone")
        }
    }

    /// The dots go on the LEFT of the quota bars (#909's mockup ordering).
    ///
    /// Width cannot see this — `composeSideBySide` is commutative in width — so
    /// this compares the composed icon's pixels against both orderings. The
    /// first assertion is the vacuity guard: if the two orderings rendered
    /// identically, the second would prove nothing.
    ///
    /// Mutation-proved: swap the two arguments to `composeSideBySide` in
    /// `MenuBarImageBuilder.iconImage` and this goes red.
    func testComposedIconPutsTheDotsBeforeTheQuotaBars() throws {
        let sessions = MenuBarFixtures.acrossProjectsWithQuota(2)
        let appearance = MenuBarAppearance(style: .combined, isCompact: false)
        let dots = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: appearance, sessions: sessions, projectGroupOrder: []
        ))
        let quota = try XCTUnwrap(MenuBarImageBuilder.quotaImage(
            appearance: appearance, sessions: sessions, providerKey: nil, now: fixedNow
        ))
        let gap = MenuBarStatusRenderer.groupGap
        let dotsFirst = try XCTUnwrap(MenuBarImageBuilder.composeSideBySide(dots, quota, gap: gap))
        let quotaFirst = try XCTUnwrap(MenuBarImageBuilder.composeSideBySide(quota, dots, gap: gap))

        XCTAssertNotEqual(dotsFirst.tiffRepresentation, quotaFirst.tiffRepresentation,
                          "the two orderings must render differently, or the check below "
                              + "cannot distinguish them")
        let composed = try XCTUnwrap(icon(appearance, sessions: sessions))
        XCTAssertEqual(composed.tiffRepresentation, dotsFirst.tiffRepresentation,
                       "the icon must compose dots first, quota bars second")
    }

    // MARK: - iconState priority order (untouched by #1862)

    /// An errored session must NOT promote the icon to `.attention`, which is
    /// a FULL REPLACEMENT of the dots. `iconState` never inspects session
    /// state at all — this LOCKs its untouched priority order, unaffected by
    /// either #1802 or #1862.
    func testErrorDoesNotPromoteToTheAttentionIcon() {
        XCTAssertEqual(MenuBarImageBuilder.iconState(pendingConsentCount: 0, sessionCount: 3), .dots)
        XCTAssertEqual(MenuBarImageBuilder.iconState(pendingConsentCount: 1, sessionCount: 3), .attention)
    }
}
