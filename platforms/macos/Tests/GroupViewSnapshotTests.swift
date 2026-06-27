import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

@MainActor
final class GroupViewSnapshotTests: XCTestCase {
    private var sessionManager: SessionManager!
    private var originalShowCostDisplay: Any?
    private var originalProjectCostTimeframe: Any?

    override func setUp() async throws {
        try await super.setUp()
        // GroupView uses @AppStorage (UserDefaults.standard), so we can't isolate
        // via suiteName. Snapshot the real keys and restore them in tearDown so
        // the developer's defaults aren't mutated by test runs.
        originalShowCostDisplay = UserDefaults.standard.object(forKey: "showCostDisplay")
        originalProjectCostTimeframe = UserDefaults.standard.object(forKey: "projectCostTimeframe")
        UserDefaults.standard.set(false, forKey: "showCostDisplay")
        UserDefaults.standard.set("day", forKey: "projectCostTimeframe")
        sessionManager = SessionManager()
    }

    override func tearDown() async throws {
        if let value = originalShowCostDisplay {
            UserDefaults.standard.set(value, forKey: "showCostDisplay")
        } else {
            UserDefaults.standard.removeObject(forKey: "showCostDisplay")
        }
        if let value = originalProjectCostTimeframe {
            UserDefaults.standard.set(value, forKey: "projectCostTimeframe")
        } else {
            UserDefaults.standard.removeObject(forKey: "projectCostTimeframe")
        }
        try await super.tearDown()
    }

    private func makeSession(id: String) -> SessionState {
        SessionState(
            id: id,
            state: .working,
            model: "claude-opus-4-7",
            cwd: "/Users/test/projects/app",
            transcriptPath: nil,
            gitBranch: "main",
            projectName: "app",
            firstSeen: Date(timeIntervalSince1970: 1_700_000_000),
            updatedAt: Date(timeIntervalSince1970: 1_700_000_000),
            eventCount: 0,
            lastEvent: nil
        )
    }

    private func makeGroup(name: String, sessions: Int = 2) -> SessionManager.AgentGroup {
        SessionManager.AgentGroup(
            name: name,
            agents: (0..<sessions).map { makeSession(id: "\(name)-\($0)") }
        )
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

    private func seedThreeGroups() -> [SessionManager.AgentGroup] {
        let groups = [makeGroup(name: "alpha"), makeGroup(name: "beta"), makeGroup(name: "gamma")]
        sessionManager.apiGroups = groups
        return groups
    }

    func testFirstOfThree_UpChevronDisabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[0]))
        assertSnapshot(of: view, as: .image)
    }

    func testMiddleOfThree_BothChevronsEnabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[1]))
        assertSnapshot(of: view, as: .image)
    }

    func testLastOfThree_DownChevronDisabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[2]))
        assertSnapshot(of: view, as: .image)
    }

    func testSingleGroup_NoChevrons() {
        let solo = makeGroup(name: "solo")
        sessionManager.apiGroups = [solo]
        let view = host(GroupView(group: solo))
        assertSnapshot(of: view, as: .image)
    }

    func testSubGroup_NoChevrons() {
        _ = seedThreeGroups()
        let view = host(GroupView(group: makeGroup(name: "nested"), depth: 1))
        assertSnapshot(of: view, as: .image)
    }

    /// A transient PID=0 antigravity ghost (ready, no metrics) sitting alongside
    /// a substantive working session in one group — the list-level view an agent
    /// checks to confirm a ghost row renders without disturbing its neighbours
    /// (issue #757).
    func testGhostAlongsideRealSessions() {
        let real = makeSession(id: "real-working")
        let ghost = SessionState(
            id: "proc-0",
            state: .ready,
            model: "gemini-3-pro",
            cwd: "/Users/test/projects/app",
            transcriptPath: nil,
            gitBranch: "main",
            projectName: "app",
            firstSeen: Date(timeIntervalSince1970: 1_700_000_000),
            updatedAt: Date(timeIntervalSince1970: 1_700_000_000),
            eventCount: 0,
            lastEvent: nil,
            metrics: nil,
            pid: 0,
            adapter: "antigravity"
        )
        let group = SessionManager.AgentGroup(name: "app", agents: [real, ghost])
        sessionManager.apiGroups = [group]
        let view = host(GroupView(group: group), height: 160)
        assertSnapshot(of: view, as: .image)
    }
}
