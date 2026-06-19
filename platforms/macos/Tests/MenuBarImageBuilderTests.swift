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
}
