import Foundation
@testable import Irrlicht

/// The session fixtures the two menu-bar suites both build icons from.
///
/// `MenuBarAppearanceTests` and `MenuBarImageBuilderTests` assert the SAME
/// derived widths — 18.50 for the aggregate dot, 90.00 for the per-project
/// layout, and the quota half's own constants — one through the extracted
/// seams and one through the composed icon. Two private copies of the fixture
/// is how those two suites quietly stop testing the same thing:
/// `TestAgentBranding`'s own doc states the rule this file follows, that "a
/// change applied to one copy and not the other would leave one suite green
/// against a fixture the other no longer uses".
///
/// That risk is not hypothetical here. `testShippedStylesRenderExactlyWhatThey`
/// `DidBeforeAtTheDefaults` is the named compatibility LOCK inherited from
/// #1852 and `testComposedIconForEveryStyleAndGrouping` is the composed-icon
/// check that closed four surviving mutations; both pin the same numbers, and
/// they must be pinning them against the same sessions.
enum MenuBarFixtures {

    /// The instant every fixture here is built relative to.
    /// `PinnedNowSnapshot.referenceNow` rather than a local literal, so these
    /// sessions sit at the same place on the timeline as
    /// `QuotaMenuBarRendererTests`' — the suite whose constants these widths
    /// are derived from.
    static let now = PinnedNowSnapshot.referenceNow

    /// `daemonID` is the relay daemon that reported the session — `nil` for one
    /// this Mac's own daemon reported, which is what
    /// `MenuBarStatusRenderer`'s `location` bucketing keys on (#1955 phase 2).
    /// Stamped after construction because `SessionState.daemonID` is not an
    /// `init` parameter (`SessionState.swift:914` declares it as a defaulted
    /// `var`), which is also how `SessionRowSnapshotTests` sets it.
    ///
    /// It is a UNIT-level fixture: `location` behaviour that depends on the
    /// relay INGEST — the `daemonID` stamping and the label maps — is driven
    /// through `RelayFixtures` and a real `SessionManager` instead, because a
    /// hand-stamped id cannot catch the ingest changing where it puts one.
    static func session(
        id: String,
        state: SessionState.State = .working,
        project: String,
        parentSessionId: String? = nil,
        daemonID: String? = nil
    ) -> SessionState {
        var session = SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/\(project)",
            projectName: project,
            firstSeen: now,
            updatedAt: now,
            parentSessionId: parentSessionId
        )
        session.daemonID = daemonID
        return session
    }

    /// Six sessions in six DISTINCT projects across three locations — two on
    /// this Mac, two on `d-alpha`, two on `d-beta` — plus the labels the relay
    /// would have announced for those two daemons (#1955 phase 2).
    ///
    /// Distinct projects throughout is what makes it discriminating: `project`
    /// bucketing yields six buckets and `location` yields three, so a test
    /// built on it can tell the two apart. Shared rather than copied per suite
    /// for the reason at the top of this file — `MenuBarAppearanceTests` and
    /// `MenuBarStatusRendererTests` both assert bucket COUNTS and bucket ORDER
    /// against it, and two copies is how one of them would quietly start
    /// asserting against a different world.
    ///
    /// Unit-level: it stamps `daemonID` directly. The ingest-level version —
    /// where the ids and the labels come from real relay frames — is
    /// `MenuBarAppearanceTests.managerWithTwoDaemonsAndOneLocalSession`, and a
    /// hand-stamped id cannot catch the ingest moving where it puts one.
    static func acrossThreeLocations() -> [SessionState] {
        [
            session(id: "l1", project: "local-one"),
            session(id: "l2", project: "local-two"),
            session(id: "a1", project: "alpha-one", daemonID: "d-alpha"),
            session(id: "a2", project: "alpha-two", daemonID: "d-alpha"),
            session(id: "b1", project: "beta-one", daemonID: "d-beta"),
            session(id: "b2", project: "beta-two", daemonID: "d-beta"),
        ]
    }

    /// The labels a relay `snapshot` announces for `acrossThreeLocations`' two
    /// daemons.
    static let threeLocationLabels = ["d-alpha": "alpha-box", "d-beta": "beta-box"]

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
    ///
    /// `adapter` selects which provider the snapshot buckets under —
    /// `RateLimitInfo.providerKey(adapter:)` maps `claude-code` to `anthropic`
    /// and `codex` to `openai`. The parameters exist for #1955's ordered
    /// provider slots, which need TWO providers that render DIFFERENTLY: with
    /// identical fills the two slots produce identical pixels, and an
    /// ordering assertion over them would pass whatever the order was. The
    /// `claude-code` defaults reproduce the fixture exactly as it stood before
    /// #1955, so every existing caller keeps its numbers.
    static func sessionWithQuota(
        adapter: String = "claude-code",
        fiveHourPercent: Double = 20,
        sevenDayPercent: Double = 40
    ) -> SessionState {
        // Derived from the adapter rather than taken as two more parameters:
        // the id and the project only ever need to be DISTINCT per provider,
        // and deriving them makes that true by construction instead of by the
        // caller remembering to vary them.
        let project = adapter == "claude-code" ? "quota" : "quota-\(adapter)"
        let id = adapter == "claude-code" ? "sess_quota" : "sess_quota_\(adapter)"
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
                        usedPercent: fiveHourPercent, windowMinutes: 300,
                        resetsAt: now.addingTimeInterval(3600)
                    ),
                    RateLimitWindowInfo(
                        usedPercent: sevenDayPercent, windowMinutes: 10080,
                        resetsAt: now.addingTimeInterval(3 * 86400)
                    ),
                ],
                sampledAt: now
            )
        )
        return SessionState(
            id: id,
            state: .working,
            model: "claude-sonnet",
            cwd: "/Users/test/projects/\(project)",
            projectName: project,
            firstSeen: now,
            updatedAt: now,
            metrics: metrics,
            adapter: adapter
        )
    }

    /// `projects` projects in total, exactly one of which carries a renderable
    /// quota — so BOTH halves of the icon have something to draw and the
    /// per-project dot count is still `projects`.
    static func acrossProjectsWithQuota(_ projects: Int) -> [SessionState] {
        acrossProjects(projects - 1) + [sessionWithQuota()]
    }
}
