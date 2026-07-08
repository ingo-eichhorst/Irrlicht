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

    // MARK: - shouldFallBackToDotsForUsageStyle (issue #909 review fix)

    /// `.usage` style with active sessions but no renderable quota yet must
    /// not collapse to the "no sessions" icon — see the fallback comment in
    /// `combinedImage`.
    func testFallsBackToDotsWhenUsageStyleHasNoQuotaButHasSessions() {
        XCTAssertTrue(MenuBarImageBuilder.shouldFallBackToDotsForUsageStyle(
            style: .usage, quotaImage: nil, sessionCount: 3
        ))
    }

    func testDoesNotFallBackWhenUsageStyleHasQuotaImage() {
        let quota = NSImage(size: NSSize(width: 10, height: 18))
        XCTAssertFalse(MenuBarImageBuilder.shouldFallBackToDotsForUsageStyle(
            style: .usage, quotaImage: quota, sessionCount: 3
        ))
    }

    func testDoesNotFallBackWhenUsageStyleHasNoSessionsEither() {
        XCTAssertFalse(MenuBarImageBuilder.shouldFallBackToDotsForUsageStyle(
            style: .usage, quotaImage: nil, sessionCount: 0
        ))
    }

    func testDoesNotFallBackForLightsOrCombinedStyles() {
        XCTAssertFalse(MenuBarImageBuilder.shouldFallBackToDotsForUsageStyle(
            style: .lights, quotaImage: nil, sessionCount: 3
        ))
        XCTAssertFalse(MenuBarImageBuilder.shouldFallBackToDotsForUsageStyle(
            style: .combined, quotaImage: nil, sessionCount: 3
        ))
    }
}
