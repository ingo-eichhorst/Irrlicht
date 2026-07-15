import XCTest
@testable import Irrlicht

/// The Activity tab is gated behind a beta toggle (#1075). HistoryView reads the
/// flag via @AppStorage, but the decision itself is a pure function on the enum,
/// so these assert it directly rather than standing up a view and mutating the
/// developer's real UserDefaults.
final class HistoryTabVisibilityTests: XCTestCase {
    func testActivityIsHiddenByDefault() {
        XCTAssertEqual(HistoryTab.visible(activityEnabled: false), [.usage, .metrics, .quota])
    }

    func testActivityAppearsWhenEnabled() {
        XCTAssertEqual(HistoryTab.visible(activityEnabled: true), [.usage, .activity, .metrics, .quota])
    }

    func testGatingOnlyAffectsActivity() {
        // Every other tab is unconditional — the toggle must not disturb the
        // order or membership of the rest of the picker.
        let off = HistoryTab.visible(activityEnabled: false)
        let on = HistoryTab.visible(activityEnabled: true)
        XCTAssertEqual(off, on.filter { $0 != .activity })
        XCTAssertEqual(on, HistoryTab.allCases)
    }
}
