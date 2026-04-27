import XCTest
@testable import Irrlicht
import Foundation

/// Regression coverage for the WebSocket → apiGroups patch path.
///
/// Background: the menu-bar list renders from `apiGroups`, not from the flat
/// `sessions` array fed by `sessionMap`. Updates only reach the UI tree via
/// `patchApiGroups`, and that path used to drop silently when a session id
/// wasn't in `groupedSessionIds` and never walked `agent.children`. That left
/// sessions visibly stuck on stale context/cost values (see fix alongside
/// 0.3.8). These tests pin the corrected behavior.
@MainActor
final class SessionManagerApiGroupsTests: XCTestCase {
    typealias AgentGroup = SessionManager.AgentGroup

    private var sut: SessionManager!

    override func setUp() async throws {
        try await super.setUp()
        sut = SessionManager()
    }

    override func tearDown() async throws {
        sut = nil
        try await super.tearDown()
    }

    // MARK: - collectSessionIds

    func testCollectSessionIds_includesTopLevelAgents() {
        let group = AgentGroup(
            name: "articles",
            agents: [makeSession(id: "a1"), makeSession(id: "a2")]
        )
        XCTAssertEqual(Set(sut.collectSessionIds(from: group)), ["a1", "a2"])
    }

    func testCollectSessionIds_includesChildren() {
        // Regression: children were structurally unreachable — the set built
        // from `collectSessionIds` never contained child ids, so the guard in
        // `patchApiGroups` dropped every WS update for subagents.
        let parent = makeSession(
            id: "parent",
            children: [makeSession(id: "child1"), makeSession(id: "child2")]
        )
        let group = AgentGroup(name: "proj", agents: [parent])
        XCTAssertEqual(
            Set(sut.collectSessionIds(from: group)),
            ["parent", "child1", "child2"]
        )
    }

    func testCollectSessionIds_descendsIntoNestedGroups() {
        let nested = AgentGroup(name: "sub", agents: [makeSession(id: "n1")])
        let top = AgentGroup(
            name: "top",
            agents: [makeSession(id: "t1")],
            groups: [nested]
        )
        XCTAssertEqual(Set(sut.collectSessionIds(from: top)), ["t1", "n1"])
    }

    // MARK: - patchGroup

    func testPatchGroup_patchesTopLevelAgentMetrics() {
        let before = makeSession(id: "s1", cost: 1.00)
        let sibling = makeSession(id: "s2", cost: 3.00)
        let group = AgentGroup(name: "proj", agents: [before, sibling])

        let updated = makeSession(id: "s1", cost: 2.50)
        let patched = sut.patchGroup(group, with: updated)

        XCTAssertEqual(patched.agents?.first(where: { $0.id == "s1" })?.metrics?.estimatedCostUSD, 2.50)
        // Siblings untouched.
        XCTAssertEqual(patched.agents?.first(where: { $0.id == "s2" })?.metrics?.estimatedCostUSD, 3.00)
    }

    func testPatchGroup_patchesChildInsideAgent() {
        // Regression: `patchGroup` used to only patch `group.agents` and
        // `group.groups`; child sessions buried in `agent.children` were
        // unreachable and their rows never ticked until the 30 s hydration.
        let child = makeSession(id: "child", cost: 0.10)
        let parent = makeSession(id: "parent", cost: 5.00, children: [child])
        let group = AgentGroup(name: "proj", agents: [parent])

        let updatedChild = makeSession(id: "child", cost: 0.50)
        let patched = sut.patchGroup(group, with: updatedChild)

        let patchedParent = patched.agents?.first { $0.id == "parent" }
        XCTAssertEqual(patchedParent?.metrics?.estimatedCostUSD, 5.00,
                       "parent metrics must be untouched when patching a child")
        XCTAssertEqual(patchedParent?.children?.first?.metrics?.estimatedCostUSD, 0.50)
    }

    func testPatchGroup_preservesChildrenWhenPatchingParent() {
        // Regression: WS payloads don't carry `children`, so naively
        // substituting the matched agent with the incoming session would
        // drop every subagent row. `withChildren` on the patched copy
        // reattaches them.
        let childA = makeSession(id: "childA")
        let childB = makeSession(id: "childB")
        let parent = makeSession(id: "parent", cost: 1.00, children: [childA, childB])
        let group = AgentGroup(name: "proj", agents: [parent])

        // Incoming update with no children field — mirrors how the daemon
        // serializes WS deltas.
        let wsUpdate = makeSession(id: "parent", cost: 3.00, children: nil)
        let patched = sut.patchGroup(group, with: wsUpdate)

        let patchedParent = patched.agents?.first { $0.id == "parent" }
        XCTAssertEqual(patchedParent?.metrics?.estimatedCostUSD, 3.00)
        XCTAssertEqual(patchedParent?.children?.map(\.id), ["childA", "childB"])
    }

    func testPatchGroup_returnsUnchangedWhenNoMatch() {
        let group = AgentGroup(name: "proj", agents: [makeSession(id: "only")])
        let unrelated = makeSession(id: "unrelated", cost: 999)
        let patched = sut.patchGroup(group, with: unrelated)
        XCTAssertEqual(patched.agents?.map(\.id), ["only"])
        XCTAssertEqual(patched.agents?.first?.metrics?.estimatedCostUSD, 0)
    }

    // MARK: - patchApiGroups (end-to-end through SessionManager state)

    func testPatchApiGroups_updatesAgentMetricsWhenIdKnown() {
        let original = makeSession(id: "live", cost: 1.00)
        sut.apiGroups = [AgentGroup(name: "proj", agents: [original])]
        sut.groupedSessionIds = ["live"]

        let update = makeSession(id: "live", cost: 7.77)
        sut.patchApiGroups(session: update)

        XCTAssertEqual(
            sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD,
            7.77,
            "WS update for a known session must be reflected in apiGroups"
        )
    }

    func testPatchApiGroups_leavesApiGroupsUnchangedOnMiss() {
        // When the guard drops, we rely on the debounced rehydration (tested
        // indirectly by the fact that we don't crash / mutate). The important
        // invariant here is that an unknown session id never corrupts
        // apiGroups by emitting a bogus agent row.
        let original = makeSession(id: "known", cost: 1.00)
        sut.apiGroups = [AgentGroup(name: "proj", agents: [original])]
        sut.groupedSessionIds = ["known"]

        let ghost = makeSession(id: "unknown", cost: 999)
        sut.patchApiGroups(session: ghost)

        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["known"])
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD, 1.00)
    }

    // MARK: - removeFromApiGroups (regression for #244)

    func testRemoveFromApiGroups_dropsTopLevelAgentAndEmptyGroup() {
        // #244: when the daemon broadcasts session_deleted, apiGroups must
        // shed the agent synchronously so the overlay's empty-state predicate
        // (apiGroups.isEmpty && sessions.isEmpty) fires before the 0.5 s
        // debounced rehydrate. Otherwise the menu bar goes idle but the
        // overlay still shows the last session row.
        let lone = makeSession(id: "live")
        sut.apiGroups = [AgentGroup(name: "irrlicht", agents: [lone])]
        sut.groupedSessionIds = ["live"]

        sut.removeFromApiGroups(sessionId: "live")

        XCTAssertTrue(sut.apiGroups.isEmpty,
                      "agent-empty non-gas-town group must be dropped")
        XCTAssertFalse(sut.groupedSessionIds.contains("live"))
    }

    func testRemoveFromApiGroups_dropsChild_keepsParent() {
        let child = makeSession(id: "child")
        let parent = makeSession(id: "parent", cost: 2.00, children: [child])
        sut.apiGroups = [AgentGroup(name: "irrlicht", agents: [parent])]
        sut.groupedSessionIds = ["parent", "child"]

        sut.removeFromApiGroups(sessionId: "child")

        let patchedParent = sut.apiGroups.first?.agents?.first { $0.id == "parent" }
        XCTAssertNotNil(patchedParent, "parent must still be present after child removed")
        XCTAssertNil(patchedParent?.children,
                     "children should clear to nil when last child is removed")
        XCTAssertEqual(patchedParent?.metrics?.estimatedCostUSD, 2.00,
                       "parent metrics untouched")
        XCTAssertFalse(sut.groupedSessionIds.contains("child"))
        XCTAssertTrue(sut.groupedSessionIds.contains("parent"))
    }

    func testRemoveFromApiGroups_dropsParent_takesChildrenWithIt() {
        // The daemon emits a separate session_deleted for each subagent, but
        // if the parent removal lands first the agent entry (and its dangling
        // children) must vanish — we don't want orphaned subagent rows.
        let child = makeSession(id: "child")
        let parent = makeSession(id: "parent", children: [child])
        sut.apiGroups = [AgentGroup(name: "irrlicht", agents: [parent])]
        sut.groupedSessionIds = ["parent", "child"]

        sut.removeFromApiGroups(sessionId: "parent")

        XCTAssertTrue(sut.apiGroups.isEmpty,
                      "removing the only top-level agent must drop the group")
        XCTAssertFalse(sut.groupedSessionIds.contains("parent"))
    }

    func testRemoveFromApiGroups_preservesGasTownGroupWhenEmpty() {
        // Gas Town renders even with no rigs (menu-bar badge shows whenever
        // the daemon is running), so the top-level gastown group must not
        // be pruned even when its last session is removed.
        let rigSession = makeSession(id: "rig-1")
        let rigGroup = AgentGroup(name: "rig-a", agents: [rigSession])
        let gasTown = AgentGroup(
            name: "Gas Town",
            type: "gastown",
            agents: nil,
            groups: [rigGroup]
        )
        sut.apiGroups = [gasTown]
        sut.groupedSessionIds = ["rig-1"]

        sut.removeFromApiGroups(sessionId: "rig-1")

        XCTAssertEqual(sut.apiGroups.count, 1,
                       "gas-town top-level group must survive empty rigs")
        XCTAssertTrue(sut.apiGroups.first?.isGasTown ?? false)
        XCTAssertEqual(sut.apiGroups.first?.groups?.count, 0,
                       "empty rig group must be pruned")
    }

    func testRemoveFromApiGroups_recursesIntoNestedNonGasTownGroup() {
        // Locks in the recursion through `group.groups` for plain project
        // groups (the gas-town test exercises the same path but trips the
        // isGasTown carve-out, so it doesn't pin generic recursion).
        let nestedSession = makeSession(id: "deep")
        let inner = AgentGroup(name: "inner", agents: [nestedSession])
        let outer = AgentGroup(
            name: "outer",
            agents: [makeSession(id: "sibling")],
            groups: [inner]
        )
        sut.apiGroups = [outer]
        sut.groupedSessionIds = ["sibling", "deep"]

        sut.removeFromApiGroups(sessionId: "deep")

        XCTAssertEqual(sut.apiGroups.count, 1, "outer group must remain")
        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["sibling"])
        XCTAssertEqual(sut.apiGroups.first?.groups?.count, 0,
                       "now-empty inner group must be pruned")
        XCTAssertEqual(sut.groupedSessionIds, ["sibling"])
    }

    func testRemoveFromApiGroups_dropsParent_clearsOrphanedChildIds() {
        // Regression: removing a parent agent also drops its embedded
        // children from `apiGroups`, so their ids must leave
        // `groupedSessionIds` too — otherwise a late WS update for a child
        // would slip past the patchApiGroups guard with no row to land in.
        let child = makeSession(id: "child")
        let parent = makeSession(id: "parent", children: [child])
        sut.apiGroups = [AgentGroup(name: "irrlicht", agents: [parent])]
        sut.groupedSessionIds = ["parent", "child"]

        sut.removeFromApiGroups(sessionId: "parent")

        XCTAssertTrue(sut.apiGroups.isEmpty)
        XCTAssertFalse(sut.groupedSessionIds.contains("child"),
                       "orphaned child id must be cleared from groupedSessionIds")
        XCTAssertFalse(sut.groupedSessionIds.contains("parent"))
    }

    func testRemoveFromApiGroups_isNoOpForUnknownId() {
        let original = makeSession(id: "alive", cost: 1.00)
        sut.apiGroups = [AgentGroup(name: "irrlicht", agents: [original])]
        sut.groupedSessionIds = ["alive"]

        sut.removeFromApiGroups(sessionId: "ghost")

        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["alive"])
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD, 1.00)
        XCTAssertEqual(sut.groupedSessionIds, ["alive"])
    }

    // MARK: - SessionState.withChildren

    func testWithChildren_preservesIdentityAndMetrics() {
        let child = makeSession(id: "child")
        let original = makeSession(id: "parent", cost: 4.20, children: nil)
        let replaced = original.withChildren([child])

        XCTAssertEqual(replaced.id, original.id)
        XCTAssertEqual(replaced.metrics?.estimatedCostUSD, 4.20)
        XCTAssertEqual(replaced.children?.map(\.id), ["child"])
    }

    func testWithChildren_allowsClearingChildren() {
        let original = makeSession(
            id: "parent",
            cost: 1.00,
            children: [makeSession(id: "ghost")]
        )
        let cleared = original.withChildren(nil)
        XCTAssertNil(cleared.children)
        XCTAssertEqual(cleared.metrics?.estimatedCostUSD, 1.00)
    }

    // MARK: - Helpers

    private func makeSession(
        id: String,
        cost: Double = 0,
        children: [SessionState]? = nil
    ) -> SessionState {
        SessionState(
            id: id,
            state: .working,
            model: "test-model",
            cwd: "/tmp",
            firstSeen: Date(timeIntervalSince1970: 0),
            updatedAt: Date(timeIntervalSince1970: 0),
            metrics: SessionMetrics(
                elapsedSeconds: 0,
                totalTokens: 0,
                modelName: "test-model",
                contextWindow: nil,
                contextUtilization: 0,
                pressureLevel: "safe",
                contextWindowUnknown: nil,
                estimatedCostUSD: cost,
                lastAssistantText: nil,
                tasks: nil
            ),
            children: children
        )
    }
}
