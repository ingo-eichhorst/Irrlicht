import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

@MainActor
final class GroupViewSnapshotTests: XCTestCase {
    private var sessionManager: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        UserDefaults.standard.set(false, forKey: "showCostDisplay")
        UserDefaults.standard.set("day", forKey: "projectCostTimeframe")
        sessionManager = SessionManager()
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
        hosting.frame = CGRect(x: 0, y: 0, width: 350, height: height)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    func testFirstOfThree_UpChevronDisabled() {
        let view = host(GroupView(
            group: makeGroup(name: "alpha"), depth: 0, groupIndex: 0, totalGroups: 3
        ))
        assertSnapshot(of: view, as: .image)
    }

    func testMiddleOfThree_BothChevronsEnabled() {
        let view = host(GroupView(
            group: makeGroup(name: "beta"), depth: 0, groupIndex: 1, totalGroups: 3
        ))
        assertSnapshot(of: view, as: .image)
    }

    func testLastOfThree_DownChevronDisabled() {
        let view = host(GroupView(
            group: makeGroup(name: "gamma"), depth: 0, groupIndex: 2, totalGroups: 3
        ))
        assertSnapshot(of: view, as: .image)
    }

    func testSingleGroup_NoChevrons() {
        let view = host(GroupView(
            group: makeGroup(name: "solo"), depth: 0, groupIndex: 0, totalGroups: 1
        ))
        assertSnapshot(of: view, as: .image)
    }

    func testSubGroup_NoChevrons() {
        let view = host(GroupView(
            group: makeGroup(name: "nested"), depth: 1, groupIndex: 0, totalGroups: 3
        ))
        assertSnapshot(of: view, as: .image)
    }
}
