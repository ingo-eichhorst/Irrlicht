import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

/// Regression guard for the backchannel rule-card layout (#724). The "When"
/// row used to pack two `.fixedSize()` `.menu` pickers + a threshold field into
/// one HStack (~397pt) — wider than the rule card's ~288pt budget — which, since
/// SwiftUI frames don't clip, centered and clipped the entire Settings panel.
/// The fix stacks the event/threshold and the Agent scope on separate rows.
///
/// The agent picker only reaches its true width once `AgentRegistry.byName`
/// hydrates (an empty static at app start), so the test seeds it; otherwise the
/// overflow can't reproduce.
@MainActor
final class BackchannelRulesViewSnapshotTests: XCTestCase {
    private var savedRegistry: [String: AgentBranding] = [:]

    override func setUp() async throws {
        try await super.setUp()
        savedRegistry = AgentRegistry.byName
        // Seed agents with a realistically wide display name so the per-rule
        // Agent picker renders at the width that used to overflow the card.
        AgentRegistry.byName = [
            "claude-code": AgentBranding(
                name: "claude-code", displayName: "Claude Code",
                iconSVGLight: "", iconSVGDark: "", presets: ["compact"]
            ),
            "antigravity": AgentBranding(
                name: "antigravity", displayName: "Antigravity",
                iconSVGLight: "", iconSVGDark: "", presets: []
            ),
        ]
    }

    override func tearDown() async throws {
        AgentRegistry.byName = savedRegistry
        try await super.tearDown()
    }

    /// Host at the rule card's real budget inside Settings: panel 360 −
    /// 20×2 horizontal padding − 16 leading inset = 304pt.
    private func host(_ view: some View, height: CGFloat) -> NSView {
        let width: CGFloat = 304
        let wrapped = view
            .frame(width: width, height: height)
            .background(Color(NSColor.windowBackgroundColor))
        let hosting = NSHostingView(rootView: wrapped)
        hosting.appearance = NSAppearance(named: .darkAqua)
        hosting.frame = CGRect(x: 0, y: 0, width: width, height: height)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    /// A model with one rule using the given context event + threshold, plus a
    /// Compact input action — the widest steady state of the card.
    private func model(event: String, threshold: Double) -> BackchannelRulesModel {
        let model = BackchannelRulesModel()
        model.rules = [
            BackchannelRule(
                id: "r1",
                enabled: true,
                name: "Auto-compact",
                trigger: .init(event: event, threshold: threshold),
                actions: [.init(kind: BackchannelRule.actionInput, preset: BackchannelRule.presetCompact)],
                adapter: nil
            )
        ]
        return model
    }

    func testBackchannelRule_ContextPressure() {
        let view = BackchannelRulesView(model: model(event: BackchannelRule.eventContextPressure, threshold: 85))
        assertSnapshot(of: host(view, height: 220), as: .image)
    }

    /// The tokens variant — wider threshold field + "tokens" suffix must still
    /// fit the 304pt card without clipping.
    func testBackchannelRule_ContextTokens() {
        let view = BackchannelRulesView(model: model(event: BackchannelRule.eventContextTokens, threshold: 150_000))
        assertSnapshot(of: host(view, height: 220), as: .image)
    }
}
