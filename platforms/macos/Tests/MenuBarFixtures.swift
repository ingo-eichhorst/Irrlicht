import Foundation
@testable import Irrlicht

/// The session fixtures the two menu-bar suites both build icons from.
///
/// `MenuBarCompactStyleTests` and `MenuBarImageBuilderTests` assert the SAME
/// derived widths — 18.50 for the aggregate dot, 90.00 for the per-project
/// layout, and the quota half's own constants — one through the extracted
/// seams and one through the composed icon. Two private copies of the fixture
/// is how those two suites quietly stop testing the same thing:
/// `TestAgentBranding`'s own doc states the rule this file follows, that "a
/// change applied to one copy and not the other would leave one suite green
/// against a fixture the other no longer uses".
///
/// That risk is not hypothetical here. `testShippedStylesRenderExactlyWhatThey`
/// `DidBeforeWhenCompactIsOff` is #1852's named compatibility LOCK and
/// `testComposedIconForEveryStyleAndModifier` is the composed-icon check that
/// closed four surviving mutations; both pin the same numbers, and they must be
/// pinning them against the same sessions.
enum MenuBarFixtures {

    /// The instant every fixture here is built relative to.
    /// `PinnedNowSnapshot.referenceNow` rather than a local literal, so these
    /// sessions sit at the same place on the timeline as
    /// `QuotaMenuBarRendererTests`' — the suite whose constants these widths
    /// are derived from.
    static let now = PinnedNowSnapshot.referenceNow

    static func session(
        id: String,
        state: SessionState.State = .working,
        project: String,
        parentSessionId: String? = nil
    ) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/\(project)",
            projectName: project,
            firstSeen: now,
            updatedAt: now,
            parentSessionId: parentSessionId
        )
    }

    /// `count` sessions spread one-per-project, which is the layout that makes
    /// the per-project renderer widest for a given session count.
    ///
    /// None of them carries rate-limit data, so the quota half cannot render —
    /// which is exactly what makes this the fixture for the `.usage` fallback
    /// case, and what makes it the WRONG fixture for measuring an icon that is
    /// supposed to have a quota half.
    static func acrossProjects(_ count: Int) -> [SessionState] {
        (0..<count).map { session(id: "s\($0)", project: "p\($0)") }
    }

    /// A session carrying a renderable rate-limit snapshot, so the quota half
    /// of the icon has something to draw. Same shape `QuotaMenuBarRendererTests`
    /// uses.
    static func sessionWithQuota() -> SessionState {
        let metrics = SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: 0,
            modelName: "claude-sonnet",
            contextWindow: nil,
            contextUtilization: 0,
            pressureLevel: "safe",
            contextWindowUnknown: nil,
            estimatedCostUSD: nil,
            lastAssistantText: nil,
            tasks: nil,
            rateLimit: RateLimitInfo(
                windows: [
                    RateLimitWindowInfo(
                        usedPercent: 20, windowMinutes: 300,
                        resetsAt: now.addingTimeInterval(3600)
                    ),
                    RateLimitWindowInfo(
                        usedPercent: 40, windowMinutes: 10080,
                        resetsAt: now.addingTimeInterval(3 * 86400)
                    ),
                ],
                sampledAt: now
            )
        )
        return SessionState(
            id: "sess_quota",
            state: .working,
            model: "claude-sonnet",
            cwd: "/Users/test/projects/quota",
            projectName: "quota",
            firstSeen: now,
            updatedAt: now,
            metrics: metrics,
            adapter: "claude-code"
        )
    }

    /// `projects` projects in total, exactly one of which carries a renderable
    /// quota — so BOTH halves of the icon have something to draw and the
    /// per-project dot count is still `projects`.
    static func acrossProjectsWithQuota(_ projects: Int) -> [SessionState] {
        acrossProjects(projects - 1) + [sessionWithQuota()]
    }
}
