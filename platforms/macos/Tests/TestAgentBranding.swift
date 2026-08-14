import Foundation
@testable import Irrlicht

/// Adapter branding fixtures shared by the tests that render an adapter icon.
///
/// These mirror the SVGs each adapter's Go package publishes at
/// `GET /api/v1/agents` — see `core/adapters/inbound/agents/<name>/agent.go`.
/// They live here rather than being pasted into each test file because two
/// copies of the same fixture is how two tests quietly stop testing the same
/// thing: `SessionRowSnapshotTests` compares them against committed reference
/// PNGs while `AdapterIconAppearanceTests` asserts on their colours, so a
/// change applied to one copy and not the other would leave one suite green
/// against a fixture the other no longer uses. That is the same
/// two-sources-of-truth shape as #1509 itself.
///
/// Antigravity's light and dark variants differ ONLY in stroke colour — same
/// geometry, Google's lighter tonal palette for dark chrome. That is what
/// makes it the useful fixture for appearance tests, and it is why a
/// light/dark mix-up shows up as a pure recolour of the glyph rather than as
/// anything that looks like a layout bug.
enum TestAgentBranding {
    static let antigravity = AgentBranding(
        name: "antigravity",
        displayName: "Antigravity",
        iconSVGLight: """
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
          <g fill="none" stroke-width="15" stroke-linecap="round">
            <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#4285F4"/>
            <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#EA4335"/>
            <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#34A853"/>
          </g>
        </svg>
        """,
        iconSVGDark: """
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
          <g fill="none" stroke-width="15" stroke-linecap="round">
            <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#8AB4F8"/>
            <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#F28B82"/>
            <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#81C995"/>
          </g>
        </svg>
        """
    )
}
