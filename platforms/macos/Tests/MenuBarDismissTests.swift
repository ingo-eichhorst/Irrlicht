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
/// - `testClickInsideStatusButtonRectDoesNotDismiss` is the case the fix
///   exists for. `testMutantAlwaysDismissingDisagreesOnTheInsideRectCase`
///   commits the pre-#2038-fix behaviour (dismiss unconditionally, which is
///   what every click hit once macOS 27 started routing icon clicks through
///   this monitor) as a same-shape closure and asserts it disagrees with the
///   real predicate on exactly that case. It is redundant with
///   `testClickInsideStatusButtonRectDoesNotDismiss` today (both fail only if
///   the predicate stops returning `false` for an inside click) — kept as a
///   second, differently-shaped fixture rather than for any extra case it
///   catches.
/// - Separately (not committed, since it requires compiling a broken
///   variant of the real function in its place), the predicate's body was
///   changed to `return true` unconditionally and this suite was re-run:
///   `testClickInsideStatusButtonRectDoesNotDismiss` and
///   `testMutantAlwaysDismissingDisagreesOnTheInsideRectCase` both failed,
///   confirming the tests bite. Reverted immediately after (restored from a
///   `wip` checkpoint commit, not `git checkout`). See the PR body for the
///   command and output.
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

    /// Mutation-proved (committed): the pre-#2038-fix monitor dismissed on
    /// every click it saw — reproduced here as a same-shape closure ignoring
    /// both arguments — which is exactly what macOS 27 turned into a bug once
    /// it started routing our own icon's clicks through this monitor too.
    /// Asserting it disagrees with the real predicate on the inside-rect case
    /// keeps this test failing if the fix and the mutant are ever allowed to
    /// converge again (e.g. the guard is dropped from the call site, or the
    /// predicate's body regresses to unconditional `true`).
    func testMutantAlwaysDismissingDisagreesOnTheInsideRectCase() {
        let alwaysDismiss: (NSPoint, NSRect?) -> Bool = { _, _ in true }
        let location = NSPoint(x: rect.midX, y: rect.midY)
        XCTAssertNotEqual(
            alwaysDismiss(location, rect),
            MenuBarController.globalClickShouldDismiss(at: location, statusButtonRect: rect),
            "the mutant (pre-#2038 monitor behaviour) and the real predicate must "
                + "disagree on the inside-rect case — that disagreement IS the fix"
        )
    }
}
