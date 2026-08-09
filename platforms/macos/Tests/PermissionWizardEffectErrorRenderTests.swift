import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

/// Reachability guard for #1362 on macOS: `PermissionItem.effectNotice`
/// passing proves the *model* is right, not that the wizard ever puts the
/// warning on screen. These render the REAL `PermissionWizardView` and pin
/// the result, so "Granted, but not applied" cannot silently stop being
/// drawn.
///
/// Snapshot tests are not CI-gated in this repo (see AGENTS.md) — they are
/// a local regression net, and the references were reviewed by eye when
/// recorded.
///
/// An accessibility-tree walk was tried first and abandoned: SwiftUI
/// populates no AX tree inside a SwiftPM test bundle (every query returned
/// empty), which made the negative assertions pass vacuously.
@MainActor
final class PermissionWizardEffectErrorRenderTests: XCTestCase {

    private let reason = "settings.json is malformed: invalid character '}'"

    private func snapshotModel(state: String, effectError: String?) throws -> PermissionsSnapshot {
        let errField = effectError.map { ", \"effect_error\": \"\($0)\"" } ?? ""
        let json = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[
        {"key":"transcripts","kind":"observe","state":"granted","title":"Read transcripts",
        "feature_unlocked":"Live session state","touches":"~/.claude/projects","detail":"reads transcript files"},
        {"key":"hooks","kind":"modify","state":"\(state)",
        "title":"Install hooks","feature_unlocked":"Waiting detection",
        "touches":"~/.claude/settings.json","detail":"adds hook entries"\(errField)}]}]}
        """
        return try JSONDecoder().decode(PermissionsSnapshot.self, from: Data(json.utf8))
    }

    private func host(_ snap: PermissionsSnapshot, mode: PermissionWizardView.Mode) -> NSView {
        let manager = SessionManager()
        manager.permissionsSnapshot = snap
        let view = PermissionWizardView(mode: mode, lockedAgents: ["claude-code"], onClose: {})
            .environmentObject(manager)
        let hosting = NSHostingView(rootView: view)
        hosting.appearance = NSAppearance(named: .darkAqua)
        hosting.frame = CGRect(x: 0, y: 0, width: SessionListView.panelWidth, height: 420)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    /// The defect case: a grant whose Apply failed. The reference shows the
    /// toggle still ON (consent intact, never rolled back) with the red
    /// "Granted, but not applied" strip and its Retry button beneath it.
    func testFailedApplyRendersWarningAndRetry() throws {
        let view = host(try snapshotModel(state: "granted", effectError: reason), mode: .review)
        assertSnapshot(of: view, as: .image, named: "granted-apply-failed")
    }

    /// Control: the same wizard with a healthy grant draws no warning at
    /// all. Diffing the two references is what shows the strip is driven by
    /// effect_error and nothing else.
    func testHealthyGrantRendersNoWarning() throws {
        let view = host(try snapshotModel(state: "granted", effectError: nil), mode: .review)
        assertSnapshot(of: view, as: .image, named: "granted-healthy")
    }

    /// Auto mode asks about pending items only; a failed grant must still
    /// appear there, or the surface the user is looking at hides it.
    func testFailedApplyVisibleInAutoMode() throws {
        let view = host(try snapshotModel(state: "granted", effectError: reason), mode: .auto)
        assertSnapshot(of: view, as: .image, named: "auto-apply-failed")
    }
}
