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

    override func setUp() async throws {
        try await super.setUp()
        // SessionRowView reads several keys via @AppStorage (UserDefaults.standard).
        // Snapshot + restore so the developer's defaults survive the test run.
        let defaults = UserDefaults.standard
        originalDisplayMode = defaults.object(forKey: "displayMode")
        originalDebugMode = defaults.object(forKey: "debugMode")
        originalShowCost = defaults.object(forKey: "showCostDisplay")
        defaults.set("context", forKey: "displayMode")
        defaults.set(false, forKey: "debugMode")
        defaults.set(false, forKey: "showCostDisplay")
        sessionManager = SessionManager()
    }

    override func tearDown() async throws {
        let defaults = UserDefaults.standard
        restore(key: "displayMode", value: originalDisplayMode)
        restore(key: "debugMode", value: originalDebugMode)
        restore(key: "showCostDisplay", value: originalShowCost)
        _ = defaults
        try await super.tearDown()
    }

    private func restore(key: String, value: Any?) {
        if let value {
            UserDefaults.standard.set(value, forKey: key)
        } else {
            UserDefaults.standard.removeObject(forKey: key)
        }
    }

    private func makeMetrics(
        tokens: Int64 = 45_000,
        pressure: String = "safe",
        lastText: String? = nil
    ) -> SessionMetrics {
        SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: tokens,
            modelName: "claude-opus-4-7",
            contextWindow: 1_000_000,
            contextUtilization: 4.5,
            pressureLevel: pressure,
            estimatedCostUSD: nil,
            lastAssistantText: lastText,
            tasks: nil
        )
    }

    private func makeSession(state: SessionState.State, metrics: SessionMetrics?) -> SessionState {
        SessionState(
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
            metrics: metrics
        )
    }

    private func host(_ session: SessionState, height: CGFloat = 48) -> NSView {
        let view = SessionRowView(session: session, agentNumber: 1)
            .environmentObject(sessionManager)
            .frame(width: 350, height: height)
            .background(Color(NSColor.windowBackgroundColor))
        let hosting = NSHostingView(rootView: view)
        // Pin to dark aqua so snapshots don't depend on the current system
        // appearance (Color(NSColor.windowBackgroundColor) adapts otherwise).
        hosting.appearance = NSAppearance(named: .darkAqua)
        hosting.frame = CGRect(x: 0, y: 0, width: 350, height: height)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    func testWaitingState_ShowsQuestionBlock() {
        let session = makeSession(
            state: .waiting,
            metrics: makeMetrics(lastText: "Should I run the migration?")
        )
        let view = host(session, height: 72)
        assertSnapshot(of: view, as: .image)
    }

    func testContextBar_ShowsTokenLabel() {
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

    func testHistoryBar1Min_PreservesModelLabel() {
        snapshotHistoryMode("1 Min")
    }

    func testHistoryBar10Min_PreservesModelLabel() {
        snapshotHistoryMode("10 Min")
    }

    func testHistoryBar60Min_PreservesModelLabel() {
        snapshotHistoryMode("60 Min")
    }
}
