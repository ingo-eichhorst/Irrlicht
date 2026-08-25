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

    // MARK: - shouldShowDotsInUsageStyle (issue #909 review fix; #1802's error arm)

    /// `.usage` style with a renderable dots image but no quota yet must
    /// not collapse to the "no sessions" icon — see the comment in
    /// `combinedImage`. LOCK: pre-#1802 behaviour that must not change.
    func testFallsBackToDotsWhenUsageStyleHasNoQuotaButDotsRendered() {
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertTrue(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .usage, quotaImage: nil, dotsImage: dots, hasErroredSession: false
        ))
    }

    /// LOCK: with quota renderable and nothing wrong, `.usage` still means
    /// quota only.
    func testDoesNotFallBackWhenUsageStyleHasQuotaImage() {
        let quota = NSImage(size: NSSize(width: 10, height: 18))
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .usage, quotaImage: quota, dotsImage: dots, hasErroredSession: false
        ))
    }

    /// Sessions can be active (non-zero count) while `buildStatusImage` still
    /// returns nil (e.g. every session is a subagent whose parent was pruned
    /// out from under it) — the fallback must key off the actual dots image,
    /// not a raw session count that doesn't guarantee dots render.
    func testDoesNotFallBackWhenUsageStyleHasNoRenderableDotsEither() {
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .usage, quotaImage: nil, dotsImage: nil, hasErroredSession: false
        ))
    }

    func testDoesNotFallBackForLightsOrCombinedStyles() {
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .lights, quotaImage: nil, dotsImage: dots, hasErroredSession: false
        ))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .combined, quotaImage: nil, dotsImage: dots, hasErroredSession: false
        ))
    }

    // MARK: - #1802: `.usage` must not swallow the red circle

    /// The reason the error arm exists. `.usage` suppresses the dots outright,
    /// so without this the red circle is invisible for every user on that
    /// style and the feature silently does nothing for them. The dots are
    /// ADDED here, not substituted — `composeSideBySide` renders both halves,
    /// so the user's chosen quota bars are kept.
    ///
    /// A check the change ADDS, so there is no "before the fix" run.
    /// Mutation-proved: dropping `|| hasErroredSession` from
    /// `shouldShowDotsInUsageStyle` turns this red while the four LOCKs above
    /// stay green.
    func testUsageStyleShowsDotsWhenASessionIsErrored() {
        let quota = NSImage(size: NSSize(width: 20, height: 18))
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertTrue(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .usage, quotaImage: quota, dotsImage: dots, hasErroredSession: true
        ))
    }

    /// An error cannot conjure dots that do not render.
    func testNoDotsImageMeansNoDotsEvenWithAnError() {
        let quota = NSImage(size: NSSize(width: 20, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
            style: .usage, quotaImage: quota, dotsImage: nil, hasErroredSession: true
        ))
    }

    /// LOCK: the other two styles never route through this decision at all,
    /// error or not.
    func testOtherStylesAreUnaffectedByAnError() {
        let quota = NSImage(size: NSSize(width: 20, height: 18))
        let dots = NSImage(size: NSSize(width: 10, height: 18))
        for style in [MenuBarStyle.lights, .combined] {
            XCTAssertFalse(MenuBarImageBuilder.shouldShowDotsInUsageStyle(
                style: style, quotaImage: quota, dotsImage: dots, hasErroredSession: true
            ), "\(style) must not be changed by an errored session")
        }
    }

    /// An errored session must NOT promote the icon to `.attention`, which is
    /// a FULL REPLACEMENT of the dots and would hide the very circle #1802
    /// adds. LOCK on `iconState`'s untouched priority order.
    func testErrorDoesNotPromoteToTheAttentionIcon() {
        XCTAssertEqual(MenuBarImageBuilder.iconState(pendingConsentCount: 0, sessionCount: 3), .dots)
        XCTAssertEqual(MenuBarImageBuilder.iconState(pendingConsentCount: 1, sessionCount: 3), .attention)
    }
}
