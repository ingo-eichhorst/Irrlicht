import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

/// Reachability guard for #1385 on macOS: `unappliedGrantSummary` passing
/// proves the MODEL is right, not that anything is ever drawn. These render
/// the real `UnappliedGrantsBanner` and pin the result, so the passive
/// indicator cannot silently stop appearing — which is exactly the failure
/// #1362 hit on its first pass, where one of the two surfaces was missed.
///
/// Snapshot tests are not CI-gated in this repo (see AGENTS.md) — they are
/// a local regression net, and the references were reviewed by eye when
/// recorded.
@MainActor
final class UnappliedGrantsBannerRenderTests: XCTestCase {

    private let installFailed = "settings.json is malformed: invalid character '}'"
    private let versionFloor = "claude 1.2.0 is below the required 2.0.0; upgrade and grant again"

    private func grant(_ agent: String, _ display: String, _ reason: String) -> UnappliedGrant {
        UnappliedGrant(agent: agent, agentDisplayName: display,
                       key: "hooks", title: "Install hooks", reason: reason)
    }

    private func host(_ items: [UnappliedGrant]) throws -> PinnedSnapshotHost {
        let summary = try XCTUnwrap(UnappliedGrantSummary(items: items))
        // Composited over the panel's own background, because the banner's
        // fill is a 12%-alpha wash: hosted on transparency it lands on white
        // regardless of appearance, and the reference then shows contrast
        // the user never sees. The first recording of this test did exactly
        // that and made the reason text unreadable.
        let root = UnappliedGrantsBanner(summary: summary, onReview: {})
            .background(Color(NSColor.windowBackgroundColor))
        return PinnedSnapshotHost(root, width: SessionListView.panelWidth, height: 120)
    }

    /// One unapplied grant: singular headline, the cause spelled out, and a
    /// Review button — with no dismiss control anywhere on it.
    func testSingleUnappliedGrantRenders() throws {
        assertSnapshot(of: try host([grant("claude-code", "Claude Code", installFailed)]),
                       as: .pinnedImage, named: "one-unapplied")
    }

    /// Two grants failing for DIFFERENT reasons — an install failure
    /// (#1362) and a version-floor refusal (#1365). The reference is what
    /// shows the aggregate headline counting while each cause stays legible
    /// underneath it; a change that reduced this to a bare number would
    /// show up here as a diff.
    func testTwoDiagnosesRenderDistinguishably() throws {
        assertSnapshot(of: try host([
            grant("claude-code", "Claude Code", installFailed),
            grant("codex", "Codex", versionFloor),
        ]), as: .pinnedImage, named: "two-diagnoses")
    }
}

/// The banner is absent from the main panel when nothing is wrong. Kept as
/// a model-level assertion rather than a snapshot: hosting the whole
/// `SessionListView` to photograph the ABSENCE of a strip is a brittle way
/// to assert nothing.
final class UnappliedGrantsBannerAbsenceTests: XCTestCase {
    func testHealthySnapshotProducesNoBanner() {
        let snap = PermissionsSnapshot(mode: "ask", agents: [], unappliedGrants: [])
        XCTAssertNil(snap.unappliedGrantSummary,
                     "a healthy daemon must render no banner at all")
    }
}

/// Reachability for the macOS WIRING, not the banner view.
///
/// The two render tests above host `UnappliedGrantsBanner` directly, so
/// deleting the block in `SessionListView.mainPanel` that puts it on screen
/// leaves all of them green — the same gap review found on the web side by
/// mutation. This hosts the REAL `SessionListView` and photographs the
/// panel with and without an unapplied grant; the difference between the
/// two references is the wiring.
@MainActor
final class SessionListUnappliedGrantsWiringTests: XCTestCase {

    private func host(_ snap: PermissionsSnapshot?) -> PinnedSnapshotHost {
        let manager = SessionManager()
        manager.permissionsSnapshot = snap
        let view = SessionListView()
            .environmentObject(manager)
            .environmentObject(GasTownProvider())
            // Explicit, though `UpdateManager` now refuses to start Sparkle
            // under XCTest regardless (#1530): this is the call site whose
            // `true` default armed a modal NSAlert one second later and hung
            // whichever unrelated test next spun the main run loop.
            .environmentObject(UpdateManager(startingUpdater: false))
        return PinnedSnapshotHost(view, width: SessionListView.panelWidth, height: 420)
    }

    private var unapplied: PermissionsSnapshot {
        PermissionsSnapshot(mode: "ask", agents: [], unappliedGrants: [
            UnappliedGrant(agent: "claude-code", agentDisplayName: "Claude Code",
                           key: "hooks", title: "Install hooks",
                           reason: "settings.json is malformed: invalid character '}'"),
        ])
    }

    func testPanelShowsTheBannerWhenAGrantIsUnapplied() {
        assertSnapshot(of: host(unapplied), as: .pinnedImage, named: "panel-with-banner")
    }

    /// Control: the same panel with a healthy snapshot draws no strip at
    /// all. Diffing the two is what shows the banner is driven by the
    /// aggregate and nothing else.
    func testPanelShowsNoBannerWhenHealthy() {
        assertSnapshot(of: host(PermissionsSnapshot(mode: "ask", agents: [])),
                       as: .pinnedImage, named: "panel-healthy")
    }
}
