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
    private var originalProjectGroupOrder: Any?

    override func setUp() async throws {
        try await super.setUp()
        // seedLocalApiGroups → orderedGroups persists projectGroupOrder to
        // UserDefaults.standard. Snapshot + restore so the developer's real
        // group order survives the test run (same pattern as the snapshot tests).
        originalProjectGroupOrder = UserDefaults.standard.object(forKey: "projectGroupOrder")
        sut = SessionManager()
    }

    override func tearDown() async throws {
        if let originalProjectGroupOrder {
            UserDefaults.standard.set(originalProjectGroupOrder, forKey: "projectGroupOrder")
        } else {
            UserDefaults.standard.removeObject(forKey: "projectGroupOrder")
        }
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
        sut.seedLocalApiGroups([AgentGroup(name: "proj", agents: [original])])

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
        sut.seedLocalApiGroups([AgentGroup(name: "proj", agents: [original])])

        let ghost = makeSession(id: "unknown", cost: 999)
        sut.patchApiGroups(session: ghost)

        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["known"])
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD, 1.00)
    }

    // MARK: - removeFromApiGroups (regression for #244)

    func testRemoveFromApiGroups_dropsTopLevelAgentAndEmptyGroup() {
        let lone = makeSession(id: "live")
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [lone])])

        sut.removeFromApiGroups(sessionId: "live")

        XCTAssertTrue(sut.apiGroups.isEmpty,
                      "agent-empty non-gas-town group must be dropped")
        XCTAssertFalse(sut.groupedSessionIds.contains("live"))
    }

    func testRemoveFromApiGroups_dropsChild_keepsParent() {
        let child = makeSession(id: "child")
        let parent = makeSession(id: "parent", cost: 2.00, children: [child])
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [parent])])

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

    func testRemoveFromApiGroups_preservesGasTownGroupWhenEmpty() {
        // Gas-town's top-level row renders even with no rigs (menu-bar badge
        // tracks daemon presence, not session count), so it must not prune.
        let rigSession = makeSession(id: "rig-1")
        let rigGroup = AgentGroup(name: "rig-a", agents: [rigSession])
        let gasTown = AgentGroup(
            name: "Gas Town",
            type: "gastown",
            agents: nil,
            groups: [rigGroup]
        )
        sut.seedLocalApiGroups([gasTown])

        sut.removeFromApiGroups(sessionId: "rig-1")

        XCTAssertEqual(sut.apiGroups.count, 1,
                       "gas-town top-level group must survive empty rigs")
        XCTAssertTrue(sut.apiGroups.first?.isGasTown ?? false)
        XCTAssertEqual(sut.apiGroups.first?.groups?.count, 0,
                       "empty rig group must be pruned")
    }

    func testRemoveFromApiGroups_recursesIntoNestedNonGasTownGroup() {
        // Pins generic `group.groups` recursion — gas-town test exercises the
        // same path but trips the isGasTown carve-out.
        let nestedSession = makeSession(id: "deep")
        let inner = AgentGroup(name: "inner", agents: [nestedSession])
        let outer = AgentGroup(
            name: "outer",
            agents: [makeSession(id: "sibling")],
            groups: [inner]
        )
        sut.seedLocalApiGroups([outer])

        sut.removeFromApiGroups(sessionId: "deep")

        XCTAssertEqual(sut.apiGroups.count, 1, "outer group must remain")
        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["sibling"])
        XCTAssertEqual(sut.apiGroups.first?.groups?.count, 0,
                       "now-empty inner group must be pruned")
        XCTAssertEqual(sut.groupedSessionIds, ["sibling"])
    }

    func testRemoveFromApiGroups_dropsParent_clearsOrphanedChildIds() {
        // Children embedded in a removed parent vanish from the tree, so
        // their ids must also leave groupedSessionIds — otherwise a late WS
        // update slips past the patchApiGroups guard with no row to land in.
        let child = makeSession(id: "child")
        let parent = makeSession(id: "parent", children: [child])
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [parent])])

        sut.removeFromApiGroups(sessionId: "parent")

        XCTAssertTrue(sut.apiGroups.isEmpty)
        XCTAssertFalse(sut.groupedSessionIds.contains("child"),
                       "orphaned child id must be cleared from groupedSessionIds")
        XCTAssertFalse(sut.groupedSessionIds.contains("parent"))
    }

    func testRemoveFromApiGroups_isNoOpForUnknownId() {
        let original = makeSession(id: "alive", cost: 1.00)
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [original])])

        sut.removeFromApiGroups(sessionId: "ghost")

        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["alive"])
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD, 1.00)
        XCTAssertEqual(sut.groupedSessionIds, ["alive"])
    }

    // MARK: - deleteSession (regression for #287)

    func testDeleteSession_clearsBothMenuBarAndListViewSurfaces() {
        let id = UUID().uuidString
        let session = makeSession(id: id, cost: 1.00)
        sut.sessions = [session]
        sut.seedLocalApiGroups([AgentGroup(name: "proj", agents: [session])])

        sut.deleteSession(sessionId: id)

        XCTAssertTrue(sut.sessions.isEmpty)
        XCTAssertTrue(sut.apiGroups.isEmpty)
        XCTAssertFalse(sut.groupedSessionIds.contains(id))
    }

    // MARK: - resetSessionState (regression for #287)

    func testResetSessionState_flipsBothSurfacesAndPreservesFields() throws {
        let id = UUID().uuidString
        let child = makeSession(id: "\(id)-child")
        let working = SessionState(
            id: id, state: .working, model: "test-model", cwd: "/tmp",
            firstSeen: Date(timeIntervalSince1970: 0),
            updatedAt: Date(timeIntervalSince1970: 0),
            role: "test-role", children: [child]
        )
        let dir = instancesURL()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let fixturePath = dir.appendingPathComponent("\(id).json")
        try Data("{\"state\":\"working\",\"updated_at\":0}".utf8).write(to: fixturePath)
        defer { try? FileManager.default.removeItem(at: fixturePath) }

        sut.sessionMap[id] = working
        sut.sessions = [working]
        sut.seedLocalApiGroups([AgentGroup(name: "proj", agents: [working])])

        sut.resetSessionState(sessionId: id)

        XCTAssertEqual(sut.sessions.first?.state, .ready,
                       "menu-bar surface must flip to ready")
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.state, .ready,
                       "list-view surface must flip to ready")
        XCTAssertEqual(sut.sessions.first?.role, "test-role",
                       "withState must preserve role across reset")
        XCTAssertEqual(sut.sessions.first?.children?.map(\.id), ["\(id)-child"],
                       "withState must preserve children across reset")
    }

    private func instancesURL() -> URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Irrlicht/instances")
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

    // MARK: - Relay compound keying (#537)

    /// Two daemons emitting the same session_id must render as two distinct
    /// rows, keyed by the compound (daemon_id, session_id) — not merged.
    func testRelay_sameSessionId_differentDaemons_yieldsTwoRows() {
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))
        sut.handleRelayMessage(relayPush(source: "daemonB", sessionId: "proc-1234", project: "beta"))

        XCTAssertEqual(sut.sessions.count, 2, "two daemons sharing a session_id must be two rows")
        XCTAssertEqual(Set(sut.sessions.map { $0.rowID }).count, 2, "rowIDs must be distinct")
        XCTAssertEqual(Set(sut.sessions.map { $0.id }), ["proc-1234"], "bare session_id is preserved on both")
        XCTAssertEqual(Set(sut.sessions.compactMap { $0.daemonID }), ["daemonA", "daemonB"])
        // apiGroups (the list-view surface): both rows present across the groups.
        let groupAgentRowIDs = sut.apiGroups.flatMap { $0.agents ?? [] }.map { $0.rowID }
        XCTAssertEqual(Set(groupAgentRowIDs), ["daemonA/proc-1234", "daemonB/proc-1234"])
    }

    /// The same daemon re-emitting a session_id updates in place (one row),
    /// so a daemon reached over both paths never doubles.
    func testRelay_sameDaemonSameId_updatesInPlace() {
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha", state: "waiting"))

        XCTAssertEqual(sut.sessions.count, 1)
        XCTAssertEqual(sut.sessions.first?.state, .waiting)
    }

    /// A relay session whose id is also present locally collapses to the local
    /// row (local wins) — the v0 "same daemon over both paths shows once" guard.
    func testRelay_idAlsoPresentLocally_collapsesToLocal() {
        sut.sessionMap["proc-1234"] = makeSession(id: "proc-1234")     // local, no daemonID
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))

        XCTAssertEqual(sut.sessions.filter { $0.id == "proc-1234" }.count, 1, "local wins; relay copy suppressed")
        XCTAssertNil(sut.sessions.first { $0.id == "proc-1234" }?.daemonID, "the surviving row is the local one")
    }

    // MARK: - Origin glyph (#538)

    /// A relay session carries a daemonID, and the relay's snapshot label map
    /// resolves it to a hostname — the data the per-row origin glyph + tooltip
    /// render from. A local session has no daemonID (no glyph).
    func testOriginGlyph_remoteSessionResolvesHostnameTooltip() {
        sut.handleRelayMessage(relaySnapshot(daemonID: "daemonA", label: "ingo-mini.local"))
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))

        let remote = sut.sessions.first { $0.id == "proc-1234" }
        XCTAssertEqual(remote?.daemonID, "daemonA", "relay session carries the daemon id (glyph shows)")
        XCTAssertEqual(sut.relayDaemons["daemonA"], "ingo-mini.local", "daemon id resolves to the hostname tooltip")

        let local = makeSession(id: "local-1")
        XCTAssertNil(local.daemonID, "a local session has no daemonID — no glyph")
    }

    // MARK: - Offline fade (#540)

    /// A relay daemon going offline fades its rows (keeps them, marks offline)
    /// instead of deleting them; reconnect restores them solid.
    func testOffline_disconnectFadesRows_reconnectRestores() {
        sut.handleRelayMessage(relaySnapshot(daemonID: "daemonA", label: "ingo-mini.local"))
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))
        let row = { self.sut.sessions.first { $0.id == "proc-1234" } }
        XCTAssertNotNil(row(), "row present while the daemon is connected")
        XCTAssertFalse(sut.isOffline(row()!), "not offline while connected")

        // Daemon drops — the row stays, marked offline (faded), not deleted.
        sut.handleRelayMessage(daemonStatus(daemonID: "daemonA", status: "disconnected"))
        XCTAssertNotNil(row(), "row must remain after disconnect (fade, don't delete)")
        XCTAssertTrue(sut.isOffline(row()!), "row is offline/faded after disconnect")
        XCTAssertEqual(sut.offlineDaemons["daemonA"], "ingo-mini.local", "offline label kept for the tooltip")
        XCTAssertNil(sut.relayDaemons["daemonA"], "an offline daemon is not in the connected set")

        // Reconnect — offline mark clears; fresh state re-arrives as a push.
        sut.handleRelayMessage(daemonStatus(daemonID: "daemonA", status: "connected", label: "ingo-mini.local"))
        XCTAssertNil(sut.offlineDaemons["daemonA"], "reconnect clears the offline mark")
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))
        XCTAssertNotNil(row(), "row restored after reconnect")
        XCTAssertFalse(sut.isOffline(row()!), "row is solid again after reconnect")
    }

    // MARK: - Helpers

    /// Builds a relay `snapshot` control frame announcing a connected daemon
    /// and its hostname label (populates `relayDaemons`).
    private func relaySnapshot(daemonID: String, label: String) -> String {
        """
        {"type":"snapshot","daemons":[{"daemon_id":"\(daemonID)","daemon_label":"\(label)","status":"connected"}]}
        """
    }

    /// Builds a relay `daemon_status` control frame (connect/disconnect).
    private func daemonStatus(daemonID: String, status: String, label: String = "") -> String {
        """
        {"type":"daemon_status","daemon_id":"\(daemonID)","daemon_label":"\(label)","status":"\(status)"}
        """
    }

    /// Builds a relay Push frame (envelope `source` + inner session_created)
    /// as the hub would forward it.
    private func relayPush(source: String, sessionId: String, project: String, state: String = "working") -> String {
        """
        {"type":"push","source":"\(source)","msg":{"type":"session_created",\
        "session":{"session_id":"\(sessionId)","state":"\(state)","model":"m",\
        "cwd":"/tmp","project_name":"\(project)","first_seen":0,"updated_at":0}}}
        """
    }

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
