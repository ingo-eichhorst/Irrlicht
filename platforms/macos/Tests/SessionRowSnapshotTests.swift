import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

@MainActor
final class SessionRowSnapshotTests: XCTestCase {
    private var sessionManager: SessionManager!
    private var savedAgentRegistry: [String: AgentBranding] = [:]

    /// This suite's own preference store (#1662). It replaces a `setUp` that
    /// overwrote six keys in the REAL `com.apple.dt.xctest.tool` domain —
    /// `displayMode`, `debugMode`, `showCostDisplay`, both
    /// `ContextPressureThreshold` keys and `summaryDisplayMode` — and a
    /// `tearDown` that put them back, which the runs that abort (#1523), get
    /// their tree killed at 240s or run out of `--budget` never reached.
    ///
    /// Empty on purpose: every one of those keys resolves to the value the
    /// deleted `setUp` was assigning. `debugMode` and `showCostDisplay` default
    /// to `false`; `ContextPressureThreshold` defaults to 80 percent (#689);
    /// `displayMode` defaults to `DisplayMode.context`, which is what the old
    /// pin's lowercase `"context"` decoded to as well (it is not a valid raw
    /// value — `DisplayMode.context.rawValue` is `"Context"` — so it fell back
    /// to the same case); and `SessionManager` reads `summaryDisplayMode` from
    /// this store, where absent means `.waiting`, the pre-#985 default the old
    /// `setUp` pinned. So no reference moved.
    private let defaults = InMemoryDefaults()

    override func setUp() async throws {
        try await super.setUp()
        // SessionManager() no longer hydrates AgentRegistry from a live daemon
        // under XCTest (issue #832) — seed the branding entries the fixtures
        // below render (antigravity ghost rows, a claude-code working row) so
        // they show the real brand icon deterministically instead of racing a
        // network call. Mirrors the SVGs in
        // core/adapters/inbound/agents/{antigravity,claudecode}/agent.go.
        savedAgentRegistry = AgentRegistry.byName
        AgentRegistry.byName["antigravity"] = TestAgentBranding.antigravity
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
        sessionManager = SessionManager(defaults: defaults)
    }

    override func tearDown() async throws {
        AgentRegistry.byName = savedAgentRegistry
        try await super.tearDown()
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
        cacheBloatPercent: Int? = nil,
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
            cacheBloatPercent: cacheBloatPercent,
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
        daemonID: String? = nil,
        error: SessionError? = nil
    ) -> SessionState {
        var session = SessionState(
            id: "sess_row_test",
            state: state,
            model: "claude-opus-4-7",
            cwd: "/Users/test/projects/app",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
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
            background: background,
            error: error
        )
        // daemonID is stamped after construction by SessionManager from the relay
        // envelope; mirror that here so the relay cloud indicator can render.
        session.daemonID = daemonID
        return session
    }

    private func host(_ session: SessionState, height: CGFloat = 48) -> PinnedSnapshotHost {
        host(SessionRowView(session: session, agentNumber: 1), height: height)
    }

    private func host(_ view: some View, height: CGFloat = 48) -> PinnedSnapshotHost {
        host(view, height: height, appearance: .darkAqua)
    }

    private func hostLight(_ session: SessionState, height: CGFloat = 48) -> PinnedSnapshotHost {
        host(SessionRowView(session: session, agentNumber: 1), height: height, appearance: .aqua)
    }

    private func host(_ view: some View, height: CGFloat = 48, appearance: NSAppearance.Name) -> PinnedSnapshotHost {
        // Appearance is passed explicitly so snapshots don't depend on the
        // current system appearance (Color(NSColor.windowBackgroundColor)
        // adapts otherwise) — most tests pin dark aqua; the light-mode pill
        // contrast tests below (issue #984) pin aqua instead. The host also
        // pins the locale (#1630) and the preference store every `@AppStorage`
        // in the subtree resolves through (#1662).
        PinnedSnapshotHost(
            view
                .environmentObject(sessionManager)
                .frame(width: 350, height: height)
                .background(Color(NSColor.windowBackgroundColor)),
            width: 350, height: height, appearance: appearance, defaults: defaults)
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
            // #1802 — the error row joins the alignment stack. Its glyph is an
            // SF Symbol like `ready`'s, so it is exactly the shape #596 was
            // about: an unclamped symbol box would shift this row's agent
            // number and context bar against its neighbours.
            SessionRowView(session: makeSession(state: .error, metrics: makeMetrics()), agentNumber: 4)
        }
        let view = host(rows, height: 192)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testWaitingStateShowsQuestionBlock() {
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = host(session, height: 72)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    /// Issue #979 — the question pill used to be pinned to a single line
    /// while the daemon separately cut the source text at 70 runes, so a
    /// long either/or question lost its second option. Both caps are gone
    /// now: this pins the pill actually wrapping the full text across up to
    /// 3 lines instead of clipping it.
    func testLongQuestionWrapsAcrossMultipleLines() {
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(
                lastText: "Should I resolve the merge conflict and patch the failing test cell now, or dig into the design decision for #905/#906 first?"
            )
        )
        let view = host(session, height: 96)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    /// Issue #984 — the question pill's text color used to be a fixed hex
    /// that measured under WCAG AA (down to 2.03:1) against the light-
    /// appearance wash. Pinning aqua here (every other test pins dark aqua)
    /// guards the light-mode leg of that fix, which otherwise has no
    /// snapshot coverage at all.
    func testQuestionPillReadableInLightMode() {
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = hostLight(session, height: 72)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testCollapsedHidesSummaryBlocks() {
        // Global mode = collapsed: a waiting session's pending question shows
        // nothing — collapse applies to every row, including new entries
        // (issue #763).
        sessionManager.summaryDisplayMode = .collapsed
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = host(session, height: 48)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    /// Issue #985 — waiting mode gates the question pill by session state: a
    /// working session's pending-looking text stays hidden, so sessions
    /// blocked on the user aren't buried among working/ready rows.
    func testWaitingModeHidesNonWaitingSummary() {
        sessionManager.summaryDisplayMode = .waiting
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = host(session, height: 48)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testContextBarShowsTokenLabel() {
        let session = makeSession(state: .working, metrics: makeMetrics())
        let view = host(session)
        assertSnapshot(of: view, as: .pinnedImage)
    }

    private func sampleHistory() -> [String] {
        Array(repeating: "ready", count: 80)
            + Array(repeating: "working", count: 40)
            + Array(repeating: "waiting", count: 20)
            + Array(repeating: "working", count: 10)
    }

    private func snapshotHistoryMode(_ mode: String, testName: String = #function) {
        defaults.set(mode, forKey: "displayMode")
        let session = makeSession(state: .working, metrics: makeMetrics())
        sessionManager.stateHistory[session.id] = sampleHistory()
        let view = host(session)
        assertSnapshot(of: view, as: .pinnedImage, testName: testName)
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
    ///
    /// Issue #1509: this and testFixtureAntigravityGhost both render the 14×14
    /// antigravity SVG icon, and both used to fail with a diff confined to it.
    /// That was diagnosed twice as toolchain antialiasing drift (#1034,
    /// #1044) and treated by regenerating the references. It was not drift:
    /// `adapterIcon` resolved its light/dark brand variant from
    /// `NSApp.effectiveAppearance` — the process's appearance — while `host()`
    /// below pins the *view* to `.darkAqua`, so on a Mac with macOS
    /// auto-appearance the icon changed colour with the time of day and these
    /// two tests were red at night and green by day. The reference PNG's own
    /// history shows it oscillating rather than drifting: LIGHT (ade90bdc) →
    /// DARK (b7e33c06) → LIGHT (e77e3a83). Each "regeneration" simply re-pinned
    /// whichever variant the machine happened to be in.
    ///
    /// The icon now follows the appearance of the view it is drawn into, so
    /// this render is appearance-independent; `AdapterIconAppearanceTests`
    /// locks that directly and does not depend on the machine's own setting.
    /// A diff confined to the icon is therefore a real regression now — do not
    /// reach for `SNAPSHOT_TESTING_RECORD` before reading it.
    func testGhostRowPID0NilMetrics() {
        let session = makeSession(state: .ready, metrics: nil, pid: 0, adapter: "antigravity")
        assertSnapshot(of: host(session), as: .pinnedImage)
    }

    // MARK: - Badges and markers

    func testSubagentCountBadge() {
        let row = SessionRowView(
            session: makeSession(state: .working, metrics: makeMetrics()),
            agentNumber: 1,
            activeSubagentCount: 3
        )
        assertSnapshot(of: host(row), as: .pinnedImage)
    }

    func testBackgroundMoonDetached() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(),
            background: BackgroundAgent(name: "nightly refactor", detached: true)
        )
        assertSnapshot(of: host(session), as: .pinnedImage)
    }

    func testBackgroundMoonNonDetached() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(),
            background: BackgroundAgent(name: "nightly refactor", detached: false)
        )
        assertSnapshot(of: host(session), as: .pinnedImage)
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
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
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
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    func testContextPressureAlert() {
        // utilization 92% ≥ the pinned 80% threshold, working state → alert row.
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(tokens: 920_000, pressure: "critical", utilization: 92)
        )
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    // MARK: - #1802: the red error line under the session

    /// The rich shape: the agent's own message plus every number it reported.
    /// This is the row the feature was asked for — a red line under the
    /// session saying what went wrong.
    func testErrorRowWithFullDetail() {
        let session = makeSession(
            state: .error,
            metrics: makeMetrics(),
            error: SessionError(
                phase: "retrying", class: "rate_limit",
                message: "Overloaded — the provider is rejecting requests",
                httpStatus: 429, attempt: 3, maxAttempts: 10, retryInMs: 616.45
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    /// The shape the recordings say is common: a real failure carrying no
    /// numbers at all (claudecode's terminal `isApiErrorMessage`, copilot's
    /// `session.error` with errorType "query"). The line must still read as an
    /// error rather than collapsing to a bare message with a stray separator.
    func testErrorRowWithMessageOnly() {
        let session = makeSession(
            state: .error,
            metrics: makeMetrics(),
            error: SessionError(
                phase: nil, class: "query",
                message: "API Error: API returned an empty or malformed response (HTTP 200)",
                httpStatus: nil, attempt: nil, maxAttempts: nil, retryInMs: nil
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    /// The daemon said the session failed but could not say why. The state is
    /// the verdict and the detail is optional, so the row must still carry a
    /// red line — a red icon with nothing under it is the silent case this
    /// feature exists to end.
    func testErrorRowWithNoDetailAtAll() {
        let session = makeSession(state: .error, metrics: makeMetrics(), error: nil)
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    /// Light mode, for the same reason #984 pinned the question pill there:
    /// `errorPillText` is a per-appearance retune and only one appearance is
    /// covered by every other test in this suite. `TokenContrastTests` proves
    /// the ratio numerically; this proves it renders.
    func testErrorRowReadableInLightMode() {
        let session = makeSession(
            state: .error,
            metrics: makeMetrics(),
            error: SessionError(
                phase: "terminal", class: "auth", message: "Invalid API key",
                httpStatus: 401, attempt: nil, maxAttempts: nil, retryInMs: nil
            )
        )
        assertSnapshot(of: hostLight(session, height: 72), as: .pinnedImage)
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
        assertSnapshot(of: host(session), as: .pinnedImage)
    }

    func testRelayCloudOnline() {
        sessionManager.relayDaemons = ["mac-studio": "Mac Studio"]
        let session = makeSession(state: .working, metrics: makeMetrics(), daemonID: "mac-studio")
        assertSnapshot(of: host(session), as: .pinnedImage)
    }

    func testRelayCloudOfflineFade() {
        sessionManager.offlineDaemons = ["mac-studio": "Mac Studio"]
        let session = makeSession(state: .ready, metrics: makeMetrics(), daemonID: "mac-studio")
        assertSnapshot(of: host(session), as: .pinnedImage)
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
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    /// All rounds reported done while the session is still working: the chip
    /// reads "wrapping up" rather than the confident "<1m left" the collapsed
    /// projection used to produce (#977). The measured post-completion tail is
    /// bimodal — usually seconds, occasionally another hour — so a sub-minute
    /// countdown claims a precision the corpus does not support. Stale marker
    /// again, so the snapshot never depends on the clock.
    func testTaskProgressChipWrappingUp() {
        let session = makeSession(
            state: .working,
            metrics: makeMetrics(
                taskEstimate: TaskEstimateInfo(
                    totalRounds: 6,
                    completedRounds: 6,
                    updatedAt: Self.stalePast,
                    source: "marker"
                )
            )
        )
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }

    // MARK: - Fixture-driven rendering (issue #757)

    /// Drives a render straight from a captured `{type, session}` websocket
    /// envelope — the antigravity PID=0 ghost that Phase 1's trace explains.
    /// See testGhostRowPID0NilMetrics for why a diff isolated to this
    /// fixture's icon used to be dismissed as toolchain drift, and why it is
    /// no longer (issue #1509).
    func testFixtureAntigravityGhost() throws {
        let session = try loadSession("antigravity-ghost.json")
        assertSnapshot(of: host(session), as: .pinnedImage)
    }

    /// Drives a render from a bare daemon `SessionState` object (no envelope) —
    /// a substantive working Claude Code session with high context fill.
    func testFixtureWorkingClaude() throws {
        let session = try loadSession("working-claude.json")
        assertSnapshot(of: host(session, height: 72), as: .pinnedImage)
    }
}
