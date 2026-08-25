import XCTest
import SwiftUI
import AppKit
import SnapshotTesting
@testable import Irrlicht

/// #1802 — the daemon-wide error banner, rendered.
///
/// Mirrors `UnappliedGrantsBannerRenderTests` deliberately, including the pair
/// of PANEL snapshots that show the strip present and absent. Diffing those two
/// is what shows the banner is driven by the health aggregate and by nothing
/// else; the SEMANTIC "healthy produces no banner" claim is asserted separately
/// as an `XCTAssertNil` in `DaemonHealthTests`, because hosting a whole panel
/// to photograph the absence of a strip is a brittle way to assert nothing.
/// Both halves are needed — one of them alone lets "no finding" and "could not
/// look" produce the same green.
@MainActor
final class DaemonErrorBannerRenderTests: XCTestCase {
    private func fault(_ id: String, _ title: String, _ reason: String) -> DaemonFault {
        DaemonFault(id: id, title: title, reason: reason)
    }

    /// Takes `appearance:` rather than having a second near-copy for light
    /// mode — the shape `SessionRowSnapshotTests` already uses for the same
    /// reason (its `host(_:height:appearance:)` plus a one-line `hostLight`).
    private func host(
        _ items: [DaemonFault], appearance: NSAppearance.Name = .darkAqua
    ) throws -> PinnedSnapshotHost {
        let summary = try XCTUnwrap(DaemonErrorSummary(items: items))
        // The window background is load-bearing, not decoration. This banner's
        // ground is a 12%-alpha wash; hosted on transparency it composites onto
        // white regardless of appearance, which is how the grants banner's
        // first recording came out unreadable.
        let root = DaemonErrorBanner(summary: summary)
            .background(Color(NSColor.windowBackgroundColor))
        return PinnedSnapshotHost(root, width: SessionListView.panelWidth,
                                  height: 120, appearance: appearance)
    }

    /// The real fault this build can report today.
    func testStalledDaemonRenders() throws {
        let items = DaemonHealth.faults(aggregate: .reconnecting,
                                            local: .init(isConfigured: true, isStalled: true),
                                            relay: .init(isConfigured: false, isStalled: false))
        assertSnapshot(of: try host(items), as: .pinnedImage, named: "daemon-unreachable")
    }

    /// Two causes must stay tellable apart. The aggregate headline collapses
    /// them into a count, so the reason text is the only thing left that
    /// distinguishes them — which is why each is rendered verbatim rather than
    /// summarised. The second entry is a hook-install failure, the shape #1802
    /// names and that will arrive here once the daemon reports it (#1801).
    func testTwoFaultsRenderDistinguishably() throws {
        let items = [
            fault("daemon/unreachable", "The Irrlicht daemon is not responding",
                  "Reconnect attempts keep failing, so the sessions below may be out of date."),
            fault("hooks/claude-code", "Hooks are not installed for Claude Code",
                  "The settings file could not be written, so state changes will be detected late."),
        ]
        assertSnapshot(of: try host(items), as: .pinnedImage, named: "two-faults")
    }

    /// Light mode: `errorPillText` is a per-appearance retune, so the dark-only
    /// captures above cover exactly half of it.
    func testBannerReadableInLightMode() throws {
        let items = DaemonHealth.faults(aggregate: .reconnecting,
                                        local: .init(isConfigured: true, isStalled: true),
                                        relay: .init(isConfigured: false, isStalled: false))
        assertSnapshot(of: try host(items, appearance: .aqua), as: .pinnedImage, named: "light-mode")
    }
}

/// #1802 — the banner is actually reachable in the panel, and only when it
/// should be. The wiring guard, hosting the REAL `SessionListView`.
///
/// Separate class from the render tests above for the same reason
/// `SessionListUnappliedGrantsWiringTests` is separate: these host a whole
/// 380×420 panel and are far more sensitive to unrelated layout change, so
/// they are classified for CI on their own terms.
@MainActor
final class SessionListDaemonErrorWiringTests: XCTestCase {
    private func host(stalled: Bool) -> PinnedSnapshotHost {
        let manager = SessionManager(defaults: InMemoryDefaults())
        manager.localConnectionStalled = stalled
        let view = SessionListView()
            .environmentObject(manager)
            .environmentObject(GasTownProvider())
            // `startingUpdater: false` is required, not tidiness: Sparkle
            // otherwise arms a modal NSAlert that hangs the run (#1530).
            .environmentObject(UpdateManager(startingUpdater: false))
        return PinnedSnapshotHost(view, width: SessionListView.panelWidth, height: 420)
    }

    func testPanelShowsTheBannerWhenTheDaemonIsStalled() {
        assertSnapshot(of: host(stalled: true), as: .pinnedImage, named: "panel-with-banner")
    }

    /// The absent half of the pair. On its own this photograph asserts nothing
    /// semantic — its evidentiary value is entirely in diffing it against
    /// `panel-with-banner`. `DaemonHealthTests.testHealthyDaemonProducesNoBanner`
    /// is the assertion that says "absent" in words.
    func testPanelShowsNoBannerWhenHealthy() {
        assertSnapshot(of: host(stalled: false), as: .pinnedImage, named: "panel-healthy")
    }
}
