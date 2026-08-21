import XCTest
@testable import Irrlicht

/// Issue #1743: a native SwiftUI `.popover(...)` renders BEHIND this app's
/// own host panel (`IrrlichtPanel`, `App/MenuBarController.swift`) instead of
/// in front of it. `SessionControlButton` (`SessionRowView.swift`) is the
/// only `.popover` in the app, and `PopoverHostReorder` is the fix — the
/// full root-cause writeup lives on that type's doc comment.
///
/// A true image-snapshot proof isn't practically achievable here: this app's
/// existing `.pinnedImage` snapshot suites rasterise a single, window-less
/// `NSHostingView` (`PinnedSnapshotHost` — see docs/swift-testing.md), which
/// cannot demonstrate window-vs-window z-order at all — there is no second
/// window in the picture. What IS achievable, and is the stronger property
/// anyway, is driving the real AppKit mechanism directly: two real
/// `NSWindow`s at the real levels this app uses, ordered the real way, with
/// z-order read back from `NSApp.orderedWindows` (AppKit's own
/// front-to-back truth), before and after the fix.
///
/// Review found two things this file's first draft didn't cover, both now
/// covered: `candidates(in:)`'s "front-most" claim was untested against more
/// than one below-host-level window (`testCandidatesPicksTheGenuinelyFrontmostOfSeveralLowerLevelWindows`,
/// `testReorderIfNeededAttachesTheGenuinelyFrontmostCandidate` — the live call
/// site had been reading `NSApp.windows`, registration order, not z-order,
/// which is exactly what those two now pin), and `attach`'s counterpart,
/// `detach`, had no test proving it actually breaks the parent/child
/// reference `addChildWindow` establishes (`testDetachRemovesTheChildWindowRelationship`).
/// Not covered, and not expected to be by this file: whether the *reported*
/// symptom (issue #1743's title says "hover pop-over") is actually this
/// click-triggered popover rather than some other floating surface — see
/// this PR's description for that residual uncertainty.
@MainActor
final class PopoverHostReorderTests: XCTestCase {

    private var windows: [NSWindow] = []

    override func setUp() {
        super.setUp()
        // `NSApp` is nil until something instantiates NSApplication, which
        // otherwise happens only as a side effect of another test class's
        // own setup — left implicit, these tests would pass in a full run
        // and fail under `--filter` on this class alone. Create it here so
        // the precondition is self-supplied (mirrors
        // AdapterIconAppearanceTests.testProcessAppearanceDoesNotChangeTheRenderedIcon).
        _ = NSApplication.shared
        XCTAssertNotNil(NSApp, "no NSApplication in the test host — these tests cannot run")
    }

    override func tearDown() {
        for window in windows {
            window.parent?.removeChildWindow(window)
            window.orderOut(nil)
        }
        windows = []
        super.tearDown()
    }

    /// A stand-in for `IrrlichtPanel`: same level, same non-activating/
    /// borderless shape (`MenuBarController.configurePanel`). Used for the
    /// `candidates(in:)` tests below, which only read `.level`/`.isVisible`
    /// and don't depend on `NSApp.orderedWindows`.
    private func makeHostPanel() -> NSPanel {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 300, height: 200),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        panel.level = PopoverHostReorder.hostLevel
        panel.isReleasedWhenClosed = false
        windows.append(panel)
        return panel
    }

    /// Same level as `makeHostPanel()`, but a plain `NSWindow` rather than
    /// an `NSPanel`. Used for the `NSApp.orderedWindows` z-order proofs
    /// below: measured on this test host, a `.nonactivatingPanel` does not
    /// reliably register in `orderedWindows` without a fully-running
    /// `NSApplication` event loop (`NSApp.run()`, which nothing here calls
    /// — `swift test` never enters it), while an ordinary `NSWindow` at the
    /// same level does. That is a property of *this test host*, not of the
    /// level-based z-order rule under test, which is agnostic to the
    /// `NSWindow` vs `NSPanel` distinction — level and child-window
    /// ordering are both plain `NSWindow` mechanics `NSPanel` inherits
    /// unchanged. The `candidates(in:)` tests above already cover the real
    /// `NSPanel` type for the property they check.
    private func makeHostWindow() -> NSWindow {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 300, height: 200),
            styleMask: [.borderless],
            backing: .buffered,
            defer: false
        )
        window.level = PopoverHostReorder.hostLevel
        window.isReleasedWhenClosed = false
        windows.append(window)
        return window
    }

    /// A stand-in for the `NSWindow` SwiftUI's `.popover(...)` creates:
    /// a plain window at the level SwiftUI gives it — `.normal` — with no
    /// special elevation and no parent/child relationship to the host.
    private func makePopoverWindow() -> NSWindow {
        let window = NSWindow(
            contentRect: NSRect(x: 20, y: 20, width: 100, height: 60),
            styleMask: [.borderless],
            backing: .buffered,
            defer: false
        )
        window.isReleasedWhenClosed = false
        windows.append(window)
        return window
    }

    /// `orderedWindows` is AppKit's front-to-back truth; index 0 is
    /// frontmost. Returns whether `subject` sits in front of `other`.
    private func isInFrontOf(_ subject: NSWindow, of other: NSWindow) -> Bool {
        let ordered = NSApp.orderedWindows
        guard let subjectIndex = ordered.firstIndex(of: subject),
              let otherIndex = ordered.firstIndex(of: other) else {
            XCTFail("expected both windows in NSApp.orderedWindows, got \(ordered)")
            return false
        }
        return subjectIndex < otherIndex
    }

    // MARK: - The underlying AppKit defect (no PopoverHostReorder involved)

    /// Proves the root cause, independent of the fix: two plain `NSWindow`s
    /// at different levels do NOT order by front/back call order — the
    /// higher-level one always wins, even when the lower-level one is
    /// ordered front *afterward*. This is what makes a `.popover` at a
    /// default level structurally unable to draw above `IrrlichtPanel`
    /// unless something explicitly overrides level-based ordering.
    func testWithoutReparentingTheHigherLevelWindowAlwaysWins() {
        let host = makeHostWindow()
        let popover = makePopoverWindow()

        host.orderFrontRegardless()
        popover.orderFrontRegardless()   // ordered AFTER the host — still loses

        XCTAssertTrue(isInFrontOf(host, of: popover),
                       "the elevated host panel should render in front of the popover-level window")
        XCTAssertFalse(isInFrontOf(popover, of: host),
                        "and the popover should NOT render in front of the host — this is issue #1743")
    }

    // MARK: - candidates(in:)

    func testCandidatesPairsTheHostWithTheFrontmostLowerLevelWindow() {
        let host = makeHostPanel()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        popover.orderFrontRegardless()

        let found = PopoverHostReorder.candidates(in: [host, popover])
        XCTAssertTrue(found?.host === host)
        XCTAssertTrue(found?.popover === popover)
    }

    /// `TooltipWindowController`'s own panel sits at level 200 — ABOVE
    /// `hostLevel` (101, `.popUpMenu`) by design (`SessionListView.swift`) —
    /// so it must never be mistaken for a popover candidate. Without this,
    /// hovering a tooltip while a popover is open could reparent the wrong
    /// window.
    func testCandidatesIgnoresAWindowAboveHostLevel() {
        let host = makeHostPanel()
        let tooltipLike = makePopoverWindow()
        tooltipLike.level = NSWindow.Level(rawValue: 200)
        host.orderFrontRegardless()
        tooltipLike.orderFrontRegardless()

        XCTAssertNil(PopoverHostReorder.candidates(in: [host, tooltipLike]),
                      "a window above hostLevel must not be treated as a popover candidate")
    }

    func testCandidatesReturnsNilWithoutAVisibleHost() {
        let popover = makePopoverWindow()
        popover.orderFrontRegardless()
        XCTAssertNil(PopoverHostReorder.candidates(in: [popover]))
    }

    func testCandidatesReturnsNilWithoutAVisiblePopover() {
        let host = makeHostPanel()
        host.orderFrontRegardless()
        XCTAssertNil(PopoverHostReorder.candidates(in: [host]))
    }

    func testCandidatesIgnoresAHiddenPopover() {
        let host = makeHostPanel()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        // popover never ordered front — stays hidden
        XCTAssertNil(PopoverHostReorder.candidates(in: [host, popover]))
    }

    /// Review finding on this fix's first draft: `candidates(in:)`'s doc
    /// comment claims "the front-most OTHER visible window", but the live
    /// call site passed `NSApp.windows` (registration order, NOT z-order) —
    /// so with two below-host candidates visible at once (e.g. a transient
    /// dismiss/re-present race, or an `NSOpenPanel` open alongside a row
    /// popover), it silently picked the wrong one. This drives
    /// `candidates(in:)` with genuine `NSApp.orderedWindows` z-order —
    /// `windowB` registered FIRST but ordered to the front LAST, so if this
    /// were reading registration order it would wrongly return `windowA`.
    func testCandidatesPicksTheGenuinelyFrontmostOfSeveralLowerLevelWindows() {
        let host = makeHostWindow()
        let windowA = makePopoverWindow()
        let windowB = makePopoverWindow()
        host.orderFrontRegardless()
        windowA.orderFrontRegardless()
        windowB.orderFrontRegardless()   // ordered front LAST -> genuinely frontmost

        let found = PopoverHostReorder.candidates(in: NSApp.orderedWindows)
        XCTAssertTrue(found?.popover === windowB,
                       "must pick the window actually on top, not whichever was created/registered first")
    }

    // MARK: - attach(host:popover:) — the fix itself

    /// The green half of the red/green pair above: after `attach`, the
    /// popover renders in front of the host, unconditionally on level.
    func testAttachMakesThePopoverRenderInFrontOfTheHost() {
        let host = makeHostWindow()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        popover.orderFrontRegardless()
        XCTAssertFalse(isInFrontOf(popover, of: host), "sanity: reproduces the defect first")

        PopoverHostReorder.attach(host: host, popover: popover)

        XCTAssertTrue(popover.parent === host)
        XCTAssertTrue(host.childWindows?.contains(popover) ?? false)
        XCTAssertTrue(isInFrontOf(popover, of: host),
                      "after attach, the popover must render in front of the host")
    }

    /// Idempotent: calling it again with the same pair is a no-op, not a
    /// second `addChildWindow` (which would otherwise grow `childWindows`
    /// unbounded across repeated popover presentations).
    func testAttachIsIdempotent() {
        let host = makeHostPanel()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        popover.orderFrontRegardless()

        PopoverHostReorder.attach(host: host, popover: popover)
        PopoverHostReorder.attach(host: host, popover: popover)

        XCTAssertEqual(host.childWindows?.filter { $0 === popover }.count, 1)
    }

    /// `reorderIfNeeded()` is the two-line live glue over the two tested
    /// primitives above — this locks that it actually performs the attach
    /// end to end, from `NSApp.orderedWindows` state, and returns the
    /// window it attached (which `SessionControlButton` retains so it can
    /// `detach` on dismiss). Not covered: the timing (SwiftUI's own
    /// deferred presentation), which is glass-box AppKit behavior no test
    /// in this file drives from the SwiftUI side.
    func testReorderIfNeededAttachesFromLiveWindowState() {
        let host = makeHostWindow()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        popover.orderFrontRegardless()

        let attached = PopoverHostReorder.reorderIfNeeded()

        XCTAssertTrue(attached === popover)
        XCTAssertTrue(popover.parent === host)
        XCTAssertTrue(isInFrontOf(popover, of: host))
    }

    /// The same registration-order-vs-z-order distinction as the
    /// `candidates(in:)` test above, driven end to end through the live
    /// call site.
    func testReorderIfNeededAttachesTheGenuinelyFrontmostCandidate() {
        let host = makeHostWindow()
        let windowA = makePopoverWindow()
        let windowB = makePopoverWindow()
        host.orderFrontRegardless()
        windowA.orderFrontRegardless()
        windowB.orderFrontRegardless()

        let attached = PopoverHostReorder.reorderIfNeeded()

        XCTAssertTrue(attached === windowB)
        XCTAssertTrue(windowB.parent === host)
        XCTAssertNil(windowA.parent, "the non-frontmost candidate must be left alone")
    }

    // MARK: - detach(_:) — the leak fix

    /// `addChildWindow` gives the parent a STRONG reference (Apple's
    /// documented behavior) — verified here rather than assumed, since it's
    /// exactly the property that makes `detach` load-bearing rather than
    /// cosmetic: without it, a popover attached once and never detached
    /// stays retained by the host across every subsequent present/dismiss
    /// cycle.
    func testDetachRemovesTheChildWindowRelationship() {
        let host = makeHostWindow()
        let popover = makePopoverWindow()
        host.orderFrontRegardless()
        popover.orderFrontRegardless()
        PopoverHostReorder.attach(host: host, popover: popover)
        XCTAssertTrue(host.childWindows?.contains(popover) ?? false, "sanity: attach must have run")

        PopoverHostReorder.detach(popover)

        XCTAssertNil(popover.parent)
        XCTAssertFalse(host.childWindows?.contains(popover) ?? false)
    }

    /// A no-op on a window that was never attached — `SessionControlButton`
    /// calls this unconditionally whenever it holds a stored reference, not
    /// only when it knows attach succeeded.
    func testDetachOnAnUnattachedWindowIsHarmless() {
        let popover = makePopoverWindow()
        popover.orderFrontRegardless()
        PopoverHostReorder.detach(popover)
        XCTAssertNil(popover.parent)
    }
}
