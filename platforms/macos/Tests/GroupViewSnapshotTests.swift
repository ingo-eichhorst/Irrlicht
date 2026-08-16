import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

@MainActor
final class GroupViewSnapshotTests: XCTestCase {
    private var sessionManager: SessionManager!

    /// This suite's own preference store, written by nothing and read by
    /// nothing else (#1662). It replaces a `setUp` that overwrote
    /// `showCostDisplay` and `projectCostTimeframe` in the REAL
    /// `com.apple.dt.xctest.tool` domain and a `tearDown` that put them back —
    /// which the runs that abort (#1523), get their tree killed at 240s or run
    /// out of `--budget` never reached, so those values were left behind for
    /// the next run to render under.
    ///
    /// Empty on purpose: every key `GroupView` reads then resolves at its own
    /// `@AppStorage` default, which is exactly what the deleted `setUp` was
    /// assigning (`false` and `CostTimeframe.day`), so no reference moved.
    private let defaults = InMemoryDefaults()

    override func setUp() async throws {
        try await super.setUp()
        sessionManager = SessionManager(defaults: defaults)
    }

    private func makeSession(id: String) -> SessionState {
        SessionState(
            id: id,
            state: .working,
            model: "claude-opus-4-7",
            cwd: "/Users/test/projects/app",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
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

    private func host(_ view: some View, height: CGFloat = 48) -> PinnedSnapshotHost {
        // `PinnedSnapshotHost` pins the appearance — so snapshots don't depend
        // on the current system appearance, which `Color(NSColor.windowBackgroundColor)`
        // otherwise adapts to — the locale (#1630) and the preference store
        // every `@AppStorage` in the subtree resolves through (#1662).
        PinnedSnapshotHost(
            view
                .environmentObject(sessionManager)
                .frame(width: 350, height: height)
                .background(Color(NSColor.windowBackgroundColor)),
            width: 350, height: height, defaults: defaults)
    }

    private func seedThreeGroups() -> [SessionManager.AgentGroup] {
        let groups = [makeGroup(name: "alpha"), makeGroup(name: "beta"), makeGroup(name: "gamma")]
        sessionManager.apiGroups = groups
        return groups
    }

    func testFirstOfThreeUpChevronDisabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[0]))
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testMiddleOfThreeBothChevronsEnabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[1]))
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testLastOfThreeDownChevronDisabled() {
        let groups = seedThreeGroups()
        let view = host(GroupView(group: groups[2]))
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testSingleGroupNoChevrons() {
        let solo = makeGroup(name: "solo")
        sessionManager.apiGroups = [solo]
        let view = host(GroupView(group: solo))
        assertSnapshot(of: view, as: .pinnedImage)
    }

    func testSubGroupNoChevrons() {
        _ = seedThreeGroups()
        let view = host(GroupView(group: makeGroup(name: "nested"), depth: 1))
        assertSnapshot(of: view, as: .pinnedImage)
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
            cwd: "/Users/test/projects/app",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
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
        assertSnapshot(of: view, as: .pinnedImage)
    }
}
