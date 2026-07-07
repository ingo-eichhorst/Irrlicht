import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

@MainActor
final class SessionRowSnapshotTests: XCTestCase {
    private var sessionManager: SessionManager!
    private var originalDisplayMode: Any?
    private var originalDebugMode: Any?
    private var originalShowCost: Any?
    private var originalThresholdValue: Any?
    private var originalThresholdUnit: Any?
    private var originalUserIntent: Any?
    private var originalSummariesCollapsed: Any?
    private var savedAgentRegistry: [String: AgentBranding] = [:]

    override func setUp() async throws {
        try await super.setUp()
        // SessionManager() no longer hydrates AgentRegistry from a live daemon
        // under XCTest (issue #832) — seed the branding entries the fixtures
        // below render (antigravity ghost rows, a claude-code working row) so
        // they show the real brand icon deterministically instead of racing a
        // network call. Mirrors the SVGs in
        // core/adapters/inbound/agents/{antigravity,claudecode}/agent.go.
        savedAgentRegistry = AgentRegistry.byName
        AgentRegistry.byName["antigravity"] = AgentBranding(
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
        AgentRegistry.byName["claude-code"] = AgentBranding(
            name: "claude-code",
            displayName: "Claude Code",
            iconSVGLight: """
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">
              <rect x="8" y="4" width="40" height="32" rx="4" fill="#D97757"/>
              <rect x="4" y="16" width="8" height="12" rx="2" fill="#D97757"/>
              <rect x="44" y="16" width="8" height="12" rx="2" fill="#D97757"/>
              <rect x="18" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
              <rect x="30" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
              <rect x="12" y="36" width="6" height="14" rx="1" fill="#D97757"/>
              <rect x="22" y="36" width="6" height="10" rx="1" fill="#D97757"/>
              <rect x="32" y="36" width="6" height="10" rx="1" fill="#D97757"/>
              <rect x="42" y="36" width="6" height="14" rx="1" fill="#D97757"/>
            </svg>
            """,
            iconSVGDark: """
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">
              <rect x="8" y="4" width="40" height="32" rx="4" fill="#D97757"/>
              <rect x="4" y="16" width="8" height="12" rx="2" fill="#D97757"/>
              <rect x="44" y="16" width="8" height="12" rx="2" fill="#D97757"/>
              <rect x="18" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
              <rect x="30" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
              <rect x="12" y="36" width="6" height="14" rx="1" fill="#D97757"/>
              <rect x="22" y="36" width="6" height="10" rx="1" fill="#D97757"/>
              <rect x="32" y="36" width="6" height="10" rx="1" fill="#D97757"/>
              <rect x="42" y="36" width="6" height="14" rx="1" fill="#D97757"/>
            </svg>
            """
        )
        // SessionRowView reads several keys via @AppStorage (UserDefaults.standard).
        // Snapshot + restore so the developer's defaults survive the test run.
        let defaults = UserDefaults.standard
        originalDisplayMode = defaults.object(forKey: "displayMode")
        originalDebugMode = defaults.object(forKey: "debugMode")
        originalShowCost = defaults.object(forKey: "showCostDisplay")
        originalThresholdValue = defaults.object(forKey: ContextPressureThreshold.valueKey)
        originalThresholdUnit = defaults.object(forKey: ContextPressureThreshold.unitKey)
        originalUserIntent = defaults.object(forKey: "userIntentDisplay")
        originalSummariesCollapsed = defaults.object(forKey: "summariesCollapsed")
        defaults.set("context", forKey: "displayMode")
        defaults.set(false, forKey: "debugMode")
        defaults.set(false, forKey: "showCostDisplay")
        defaults.set(false, forKey: "userIntentDisplay")
        // Pin the context-pressure threshold so the alert snapshot is independent
        // of the developer's Settings (issue #689 made it configurable).
        defaults.set(80, forKey: ContextPressureThreshold.valueKey)
        defaults.set(ContextPressureThreshold.Unit.percent.rawValue, forKey: ContextPressureThreshold.unitKey)
        // summariesCollapsed now persists (#799) — pin it so SessionManager's
        // init value doesn't depend on the developer's real toggle state.
        defaults.set(false, forKey: "summariesCollapsed")
        sessionManager = SessionManager()
    }

    override func tearDown() async throws {
        restore(key: "displayMode", value: originalDisplayMode)
        restore(key: "debugMode", value: originalDebugMode)
        restore(key: "showCostDisplay", value: originalShowCost)
        restore(key: ContextPressureThreshold.valueKey, value: originalThresholdValue)
        restore(key: ContextPressureThreshold.unitKey, value: originalThresholdUnit)
        restore(key: "userIntentDisplay", value: originalUserIntent)
        restore(key: "summariesCollapsed", value: originalSummariesCollapsed)
        AgentRegistry.byName = savedAgentRegistry
        try await super.tearDown()
    }

    private func restore(key: String, value: Any?) {
        if let value {
            UserDefaults.standard.set(value, forKey: key)
        } else {
            UserDefaults.standard.removeObject(forKey: key)
        }
    }

    // A fixed, far-past instant: anything timestamped here reads as "stale" to
    // the ETA chip (age > 180s) regardless of when the test runs, so progress
    // snapshots are time-invariant.
    private static let stalePast = Date(timeIntervalSince1970: 1_700_000_000)

    private func makeMetrics(
        tokens: Int64 = 45_000,
        pressure: String = "safe",
        lastText: String? = nil,
        utilization: Double = 4.5,
        contextWindowUnknown: Bool? = nil,
        summary: String? = nil,
        tasks: [SessionTask]? = nil,
        taskEstimate: TaskEstimateInfo? = nil,
        taskCompletionEta: Date? = nil,
        cacheBloat: Bool? = nil,
        cacheBloatTooltip: String? = nil,
        cacheBloatExplanation: String? = nil
    ) -> SessionMetrics {
        SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: tokens,
            modelName: "claude-opus-4-7",
            contextWindow: 1_000_000,
            contextUtilization: utilization,
            pressureLevel: pressure,
            contextWindowUnknown: contextWindowUnknown,
            estimatedCostUSD: nil,
            lastAssistantText: lastText,
            taskSummary: summary,
            tasks: tasks,
            taskEstimate: taskEstimate,
            taskCompletionEta: taskCompletionEta,
            cacheBloat: cacheBloat,
            cacheBloatTooltip: cacheBloatTooltip,
            cacheBloatExplanation: cacheBloatExplanation
        )
    }

    private func makeSession(
        state: SessionState.State,
        metrics: SessionMetrics?,
        pid: Int? = nil,
        adapter: String? = nil,
        background: BackgroundAgent? = nil,
        subagents: SubagentSummary? = nil,
        role: String? = nil,
        roleIcon: String? = nil,
        workerName: String? = nil,
        workerID: String? = nil,
        daemonID: String? = nil
    ) -> SessionState {
        var session = SessionState(
            id: "sess_row_test",
            state: state,
            model: "claude-opus-4-7",
            cwd: "/Users/test/projects/app",
            transcriptPath: nil,
            gitBranch: "main",
            projectName: "app",
            firstSeen: Date(timeIntervalSince1970: 1_700_000_000),
            updatedAt: Date(timeIntervalSince1970: 1_700_000_000),
            eventCount: 5,
            lastEvent: "UserPromptSubmit",
            metrics: metrics,
            pid: pid,
            subagents: subagents,
            adapter: adapter,
            role: role,
            roleIcon: roleIcon,
            workerName: workerName,
            workerID: workerID,
            background: background
        )
        // daemonID is stamped after construction by SessionManager from the relay
        // envelope; mirror that here so the relay cloud indicator can render.
        session.daemonID = daemonID
        return session
    }

    private func host(_ session: SessionState, height: CGFloat = 48) -> NSView {
        host(SessionRowView(session: session, agentNumber: 1), height: height)
    }

    private func host(_ view: some View, height: CGFloat = 48) -> NSView {
        let wrapped = view
            .environmentObject(sessionManager)
            .frame(width: 350, height: height)
            .background(Color(NSColor.windowBackgroundColor))
        let hosting = NSHostingView(rootView: wrapped)
        // Pin to dark aqua so snapshots don't depend on the current system
        // appearance (Color(NSColor.windowBackgroundColor) adapts otherwise).
        hosting.appearance = NSAppearance(named: .darkAqua)
        hosting.frame = CGRect(x: 0, y: 0, width: 350, height: height)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    /// Decodes a daemon-shaped session fixture (issue #757). Accepts either a
    /// bare `SessionState` object or a `{type, session}` websocket envelope —
    /// the same shape the macOS app receives over the wire — so an agent can
    /// drive a render from a captured daemon payload. Fixtures live next to this
    /// file under Fixtures/SessionRow/ and carry explicit numeric epoch
    /// first_seen/updated_at for determinism.
    private func loadSession(_ name: String) throws -> SessionState {
        let dir = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures/SessionRow")
        let data = try Data(contentsOf: dir.appendingPathComponent(name))
        let decoder = JSONDecoder()
        if let env = try? decoder.decode(SessionEnvelope.self, from: data), let session = env.session {
            return session
        }
        return try decoder.decode(SessionState.self, from: data)
    }

    private struct SessionEnvelope: Decodable {
        let type: String?
        let session: SessionState?
    }

    // MARK: - Existing alignment / context / history coverage

    /// Issue #596 — one row per state, stacked: the leading state icons (and
    /// everything after them) must start at the same x in every row. The
    /// ready SF Symbol used to measure 14×14 against the others' framed
    /// 12×12, shifting ready rows 2 pt right of their neighbours.
    func testStateIconAlignmentAcrossStates() {
        let rows = VStack(spacing: 0) {
            SessionRowView(session: makeSession(state: .working, metrics: makeMetrics()), agentNumber: 1)
            SessionRowView(session: makeSession(state: .waiting, metrics: makeMetrics()), agentNumber: 2)
            SessionRowView(session: makeSession(state: .ready, metrics: makeMetrics()), agentNumber: 3)
        }
        let view = host(rows, height: 144)
        assertSnapshot(of: view, as: .image)
    }

    func testWaitingStateShowsQuestionBlock() {
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = host(session, height: 72)
        assertSnapshot(of: view, as: .image)
    }

    func testUserIntentShowsPurpleBlock() {
        // Beta "User-Intent Display" on: the task summary renders as a purple
        // block above the orange pending-question block.
        UserDefaults.standard.set(true, forKey: "userIntentDisplay")
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(
                lastText: "Should I run the migration?",
                summary: "Add OAuth login to the web dashboard"
            )
        )
        let view = host(session, height: 96)
        assertSnapshot(of: view, as: .image)
    }

    func testCollapsedHidesSummaryBlocks() {
        // Global collapse on: a waiting session with BOTH an intent summary and
        // a pending question shows neither block — collapse applies to every
        // row, including new entries (issue #763). User-intent display is on to
        // prove the purple block is hidden by collapse, not by the beta gate.
        UserDefaults.standard.set(true, forKey: "userIntentDisplay")
        sessionManager.summariesCollapsed = true
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(
                lastText: "Should I run the migration?",
                summary: "Add OAuth login to the web dashboard"
            )
        )
        let view = host(session, height: 48)
        assertSnapshot(of: view, as: .image)
    }

    func testContextBarShowsTokenLabel() {
        let session = makeSession(state: .working, metrics: makeMetrics())
        let view = host(session)
        assertSnapshot(of: view, as: .image)
    }

    private func sampleHistory() -> [String] {
        Array(repeating: "ready", count: 80)
            + Array(repeating: "working", count: 40)
            + Array(repeating: "waiting", count: 20)
            + Array(repeating: "working", count: 10)
    }

    private func snapshotHistoryMode(_ mode: String, testName: String = #function) {
        UserDefaults.standard.set(mode, forKey: "displayMode")
        let session = makeSession(state: .working, metrics: makeMetrics())
        sessionManager.stateHistory[session.id] = sampleHistory()
        let view = host(session)
        assertSnapshot(of: view, as: .image, testName: testName)
    }

    func testHistoryBar1MinPreservesModelLabel() {
        snapshotHistoryMode("1 Min")
    }

    func testHistoryBar10MinPreservesModelLabel() {
        snapshotHistoryMode("10 Min")
    }

    func testHistoryBar60MinPreservesModelLabel() {
        snapshotHistoryMode("60 Min")
    }

    // MARK: - Ghost / transient rows (issue #757)

    /// A transient PID=0 session with no metrics at all — the row must render
    /// gracefully (no token/context column) rather than crash or show garbage.
    /// (The metrics-present-but-empty shape is covered by the antigravity-ghost
    /// fixture, whose metrics object carries zero tokens / zero utilization, so a
    /// separate zero-token unit case would render an identical row.)
    func testGhostRowPID0NilMetrics() {
        let session = makeSession(state: .ready, metrics: nil, pid: 0, adapter: "antigravity")
        assertSnapshot(of: host(session), as: .image)
    }

    // MARK: - Badges and markers

    func testSubagentCountBadge() {
        let row = SessionRowView(
            session: makeSession(state: .working, metrics: makeMetrics()),
            agentNumber: 1,
            activeSubagentCount: 3
        )
        assertSnapshot(of: host(row), as: .image)
    }

    func testBackgroundMoonDetached() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(),
            background: BackgroundAgent(name: "nightly refactor", detached: true)
        )
        assertSnapshot(of: host(session), as: .image)
    }

    func testBackgroundMoonNonDetached() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(),
            background: BackgroundAgent(name: "nightly refactor", detached: false)
        )
        assertSnapshot(of: host(session), as: .image)
    }

    func testCacheBloatBadgeAttributed() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(
                cacheBloat: true,
                cacheBloatTooltip: "claude-code 2.1.143 +14K cache tokens vs 2.1.98",
                cacheBloatExplanation: "This session is creating prompt-cache tokens well above normal for this project — it's getting less benefit from caching and costing more per turn. Likely tied to claude-code 2.1.143 +14K cache tokens vs 2.1.98. Common causes: an agent update that changed context construction, large or varying pasted content each turn, or frequent context resets (e.g. /clear)."
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .image)
    }

    // #813: no version attribution → the badge falls back to a compact label
    // instead of the old bare arrow glyph.
    func testCacheBloatBadgeFallback() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(
                cacheBloat: true,
                cacheBloatTooltip: nil,
                cacheBloatExplanation: "This session is creating prompt-cache tokens well above normal for this project — it's getting less benefit from caching and costing more per turn. Common causes: an agent update that changed context construction, large or varying pasted content each turn, or frequent context resets (e.g. /clear)."
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .image)
    }

    func testContextPressureAlert() {
        // utilization 92% ≥ the pinned 80% threshold, working state → alert row.
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(tokens: 920_000, pressure: "critical", utilization: 92)
        )
        assertSnapshot(of: host(session, height: 72), as: .image)
    }

    func testRoleOrchestratorRow() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(),
            role: "witness",
            roleIcon: "👁️",
            workerName: "witness-1",
            workerID: "bead-12345678ab"
        )
        assertSnapshot(of: host(session), as: .image)
    }

    func testRelayCloudOnline() {
        sessionManager.relayDaemons = ["mac-studio": "Mac Studio"]
        let session = makeSession(state: .working, metrics: makeMetrics(), daemonID: "mac-studio")
        assertSnapshot(of: host(session), as: .image)
    }

    func testRelayCloudOfflineFade() {
        sessionManager.offlineDaemons = ["mac-studio": "Mac Studio"]
        let session = makeSession(state: .ready, metrics: makeMetrics(), daemonID: "mac-studio")
        assertSnapshot(of: host(session), as: .image)
    }

    /// Progress chip without a projection (taskCompletionEta nil): renders the
    /// time-invariant "rounds/total · percent" form. The far-past marker makes
    /// it the stale (dimmed) branch, so the snapshot never depends on the clock.
    func testTaskProgressChipStale() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(
                taskEstimate: TaskEstimateInfo(
                    totalRounds: 10,
                    completedRounds: 3,
                    updatedAt: Self.stalePast,
                    source: "marker"
                )
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .image)
    }

    // MARK: - Fixture-driven rendering (issue #757)

    /// Drives a render straight from a captured `{type, session}` websocket
    /// envelope — the antigravity PID=0 ghost that Phase 1's trace explains.
    func testFixtureAntigravityGhost() throws {
        let session = try loadSession("antigravity-ghost.json")
        assertSnapshot(of: host(session), as: .image)
    }

    /// Drives a render from a bare daemon `SessionState` object (no envelope) —
    /// a substantive working Claude Code session with high context fill.
    func testFixtureWorkingClaude() throws {
        let session = try loadSession("working-claude.json")
        assertSnapshot(of: host(session, height: 72), as: .image)
    }
}
