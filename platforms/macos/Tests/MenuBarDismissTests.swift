import XCTest
@testable import Irrlicht

/// #2038 — on macOS 27, a click on our own `NSStatusItem` also reaches the
/// global mouse-down dismiss monitor in `installDismissMonitors`, which used
/// to see only clicks delivered to OTHER apps. The monitor's `hidePanel()`
/// now runs ~90ms before the button's own `togglePanel()` action, so a
/// second click on the icon flashed the panel closed and immediately
/// reopened it instead of leaving it closed.
///
/// `MenuBarController.globalClickShouldDismiss` is the fix: a pure predicate
/// pulled out of the monitor's closure (the `applyIcon` seam pattern) that
/// skips dismissal when the click lands inside the status button's own
/// screen rect, leaving `togglePanel()` as the only thing that acts on that
/// click — exactly as on macOS ≤26, where the global monitor never saw it.
///
/// This predicate is new code with no "before" to run red against, so its
/// tests are mutation-proved rather than defect-first. They cover the
/// predicate's OWN logic only — not the `installDismissMonitors` call site
/// that invokes it, which no unit test here reaches (it needs a live
/// `NSStatusItem` window). The call-site wiring is proven by the e2e check
/// described in the PR body (synthetic `CGEvent` clicks against the built
/// app), not by anything in this file.
///
/// Mutation as run (not committed: it needs a broken build of the real
/// function): with the predicate's body changed to `return true`
/// unconditionally, `testClickInsideStatusButtonRectDoesNotDismiss` failed
/// and the other two stayed green. See the PR body for the command and output.
@MainActor
final class MenuBarDismissTests: XCTestCase {

    private let rect = NSRect(x: 100, y: 200, width: 60, height: 22)

    /// The case the fix exists for: a click on our own status item button
    /// must be left for `togglePanel()`, not dismissed by the global
    /// monitor — dismissing here is the #2038 flash.
    func testClickInsideStatusButtonRectDoesNotDismiss() {
        let location = NSPoint(x: rect.midX, y: rect.midY)
        XCTAssertFalse(
            MenuBarController.globalClickShouldDismiss(at: location, statusButtonRect: rect)
        )
    }

    /// A click anywhere else must still dismiss — this is the entire reason
    /// the monitor exists (clicking elsewhere while the panel is open closes
    /// it). Also the "lock" case: unchanged from before #2038.
    func testClickOutsideStatusButtonRectDismisses() {
        let location = NSPoint(x: rect.maxX + 50, y: rect.maxY + 50)
        XCTAssertTrue(
            MenuBarController.globalClickShouldDismiss(at: location, statusButtonRect: rect)
        )
    }

    /// No rect (button not hosted in a window, or its on-screen rect misses
    /// every `NSScreen` — the same conditions `statusButtonScreenRect()`
    /// returns nil for) always dismisses: there's no safe area to exempt, and
    /// this matches the monitor's pre-#2038 behaviour of dismissing on every
    /// click it saw.
    func testNilStatusButtonRectAlwaysDismisses() {
        let location = NSPoint(x: rect.midX, y: rect.midY)
        XCTAssertTrue(
            MenuBarController.globalClickShouldDismiss(at: location, statusButtonRect: nil)
        )
    }
}
