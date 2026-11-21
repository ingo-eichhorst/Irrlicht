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

    /// This suite's own preference store (#1662). `seedLocalApiGroups` →
    /// `orderedGroups` PERSISTS `projectGroupOrder`, which under `swift test`
    /// meant writing the developer's real `com.apple.dt.xctest.tool` domain and
    /// putting it back in a `tearDown` the aborting runs skip — measured, that
    /// domain was left holding this developer's actual project names. An
    /// injected store has nothing to restore.
    private let defaults = InMemoryDefaults()

    override func setUp() async throws {
        try await super.setUp()
        sut = SessionManager(defaults: defaults)
    }

    override func tearDown() async throws {
        sut = nil
        try await super.tearDown()
    }

    // MARK: - collectSessionIds

    func testCollectSessionIdsIncludesTopLevelAgents() {
        let group = AgentGroup(
            name: "articles",
            agents: [makeSession(id: "a1"), makeSession(id: "a2")]
        )
        XCTAssertEqual(Set(sut.collectSessionIds(from: group)), ["a1", "a2"])
    }

    func testCollectSessionIdsIncludesChildren() {
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

    func testCollectSessionIdsDescendsIntoNestedGroups() {
        let nested = AgentGroup(name: "sub", agents: [makeSession(id: "n1")])
        let top = AgentGroup(
            name: "top",
            agents: [makeSession(id: "t1")],
            groups: [nested]
        )
        XCTAssertEqual(Set(sut.collectSessionIds(from: top)), ["t1", "n1"])
    }

    // MARK: - patchGroup

    func testPatchGroupPatchesTopLevelAgentMetrics() {
        let before = makeSession(id: "s1", cost: 1.00)
        let sibling = makeSession(id: "s2", cost: 3.00)
        let group = AgentGroup(name: "proj", agents: [before, sibling])

        let updated = makeSession(id: "s1", cost: 2.50)
        let patched = sut.patchGroup(group, with: updated)

        XCTAssertEqual(patched.agents?.first(where: { $0.id == "s1" })?.metrics?.estimatedCostUSD, 2.50)
        // Siblings untouched.
        XCTAssertEqual(patched.agents?.first(where: { $0.id == "s2" })?.metrics?.estimatedCostUSD, 3.00)
    }

    func testPatchGroupPatchesChildInsideAgent() {
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

    func testPatchGroupPreservesChildrenWhenPatchingParent() {
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

    func testPatchGroupReturnsUnchangedWhenNoMatch() {
        let group = AgentGroup(name: "proj", agents: [makeSession(id: "only")])
        let unrelated = makeSession(id: "unrelated", cost: 999)
        let patched = sut.patchGroup(group, with: unrelated)
        XCTAssertEqual(patched.agents?.map(\.id), ["only"])
        XCTAssertEqual(patched.agents?.first?.metrics?.estimatedCostUSD, 0)
    }

    // MARK: - unattached_workers (#2140)
    //
    // A named orchestrator worker with no session (a free polecat, an idle
    // pool slot) arrives in its rig group's `unattached_workers`. Every group
    // rebuild must carry it, or the free-worker rows vanish on the next
    // session update until the 30 s hydrate brings them back.

    private static let freeWorker = SessionManager.UnattachedWorker(
        role: "polecat", icon: "\u{1F477}", description: "Worker agent",
        workerName: "free-2", workerID: "proj-zz99yy88", workerState: "idle"
    )

    private func decodeGroups(_ json: String) throws -> [AgentGroup] {
        try JSONDecoder().decode(SessionManager.SessionsResponse.self, from: Data(json.utf8)).groups
    }

    func testDecodeFillsUnattachedWorkers() throws {
        let groups = try decodeGroups("""
        {"groups":[{"name":"gt","type":"gastown","groups":[{"name":"irrlicht","status":"operational",
          "agents":[{"session_id":"pole-1","state":"working","role":"polecat","worker_name":"busy-1","worker_id":"proj-a1b2"}],
          "unattached_workers":[{"role":"polecat","icon":"\u{1F477}","description":"Worker agent",
            "worker_name":"free-2","worker_id":"proj-zz99yy88","worker_state":"idle"}]}]}]}
        """)
        let rig = try XCTUnwrap(groups.first?.groups?.first)
        XCTAssertEqual(rig.agents?.map(\.id), ["pole-1"])
        XCTAssertEqual(rig.unattachedWorkers, [Self.freeWorker])
    }

    func testDecodeAllIdleRigWithoutAgentsKey() throws {
        // The daemon omits an empty `agents` (json omitempty), so an all-idle
        // Gas Town arrives with no agents key at either level.
        let groups = try decodeGroups("""
        {"groups":[{"name":"gt","type":"gastown","groups":[{"name":"quiet",
          "unattached_workers":[{"worker_name":"free-3","worker_state":"idle"}]}]}]}
        """)
        let rig = try XCTUnwrap(groups.first?.groups?.first)
        XCTAssertNil(rig.agents)
        XCTAssertEqual(rig.unattachedWorkers?.map(\.workerName), ["free-3"])
        XCTAssertEqual(sut.collectSessionIds(from: groups[0]), [],
                       "a free worker must never become a session id")
        XCTAssertTrue(sut.flattenAgents(from: groups[0]).isEmpty,
                      "a free worker must never enter sessionMap")
    }

    func testPatchGroupKeepsUnattachedWorkers() {
        // The patch must hit a session INSIDE the rig: patchGroup's no-match
        // early return hands back the group untouched, field and all.
        let rig = AgentGroup(name: "irrlicht", agents: [makeSession(id: "pole-1", cost: 1)],
                             unattachedWorkers: [Self.freeWorker])
        let gasTown = AgentGroup(name: "gt", type: "gastown", groups: [rig])

        let patched = sut.patchGroup(gasTown, with: makeSession(id: "pole-1", cost: 2))

        let patchedRig = patched.groups?.first
        XCTAssertEqual(patchedRig?.agents?.first?.metrics?.estimatedCostUSD, 2,
                       "precondition: the patch reached the rig's session")
        XCTAssertEqual(patchedRig?.unattachedWorkers, [Self.freeWorker],
                       "patching a session dropped the rig's free workers")
    }

    func testPruneGroupKeepsUnattachedWorkers() {
        let rig = AgentGroup(name: "irrlicht",
                             agents: [makeSession(id: "pole-1"), makeSession(id: "pole-2")],
                             unattachedWorkers: [Self.freeWorker])
        let gasTown = AgentGroup(name: "gt", type: "gastown", groups: [rig])
        sut.seedLocalApiGroups([gasTown])

        sut.removeFromApiGroups(sessionId: "pole-1")

        let prunedRig = sut.apiGroups.first?.groups?.first
        XCTAssertEqual(prunedRig?.agents?.map(\.id), ["pole-2"], "precondition: the prune ran")
        XCTAssertEqual(prunedRig?.unattachedWorkers, [Self.freeWorker],
                       "pruning a session dropped the rig's free workers")
    }

    func testPruneGroupKeepsRigWhoseLastSessionLeftWhileWorkersRemain() {
        let rig = AgentGroup(name: "irrlicht", agents: [makeSession(id: "pole-1")],
                             unattachedWorkers: [Self.freeWorker])
        let gasTown = AgentGroup(name: "gt", type: "gastown", groups: [rig])
        sut.seedLocalApiGroups([gasTown])

        sut.removeFromApiGroups(sessionId: "pole-1")

        let rigs = sut.apiGroups.first?.groups ?? []
        XCTAssertEqual(rigs.map(\.name), ["irrlicht"],
                       "a rig with free workers must survive its last session leaving")
        XCTAssertEqual(rigs.first?.unattachedWorkers, [Self.freeWorker])
    }

    // MARK: - patchApiGroups (end-to-end through SessionManager state)

    func testPatchApiGroupsUpdatesAgentMetricsWhenIdKnown() {
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

    func testPatchApiGroupsLeavesApiGroupsUnchangedOnMiss() {
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

    func testRemoveFromApiGroupsDropsTopLevelAgentAndEmptyGroup() {
        let lone = makeSession(id: "live")
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [lone])])

        sut.removeFromApiGroups(sessionId: "live")

        XCTAssertTrue(sut.apiGroups.isEmpty,
                      "agent-empty non-gas-town group must be dropped")
        XCTAssertFalse(sut.groupedSessionIds.contains("live"))
    }

    func testRemoveFromApiGroupsDropsChildKeepsParent() {
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

    func testRemoveFromApiGroupsPreservesGasTownGroupWhenEmpty() {
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

    /// #2148: a slot's `stale` and `last_exit` decode, and decide its two
    /// tags — a non-zero exit is a failure, exit 0 and no completed run are
    /// not. A worker without either key still decodes (the third entry, and
    /// testDecodeFillsUnattachedWorkers). Mutation: seen red by removing
    /// either key from UnattachedWorker.CodingKeys (it then decodes as nil),
    /// and by dropping the `code != 0` guard in failedExitCode.
    func testDecodeUnattachedWorkerStaleAndLastExit() throws {
        let groups = try decodeGroups("""
        {"groups":[{"name":"Build pool","type":"workerlist","groups":[{"name":"team-a","status":"stale",
          "unattached_workers":[
            {"role":"worker","worker_name":"team-a-m1-1","worker_id":"proj-a1b2","worker_state":"busy","stale":true,"last_exit":2},
            {"role":"worker","worker_name":"team-a-m1-2","worker_state":"idle","last_exit":0},
            {"role":"worker","worker_name":"team-a-m1-3","worker_state":"idle"}]}]}]}
        """)
        XCTAssertTrue(groups.first?.isOrchestrator ?? false)
        XCTAssertFalse(groups.first?.isGasTown ?? true, "a worker list is not Gas Town's branding")
        let workers = try XCTUnwrap(groups.first?.groups?.first?.unattachedWorkers)
        XCTAssertEqual(workers.map(\.stale), [true, nil, nil])
        XCTAssertEqual(workers.map(\.lastExit), [2, 0, nil])
        XCTAssertEqual(workers.map(\.isStale), [true, false, false])
        XCTAssertEqual(workers.map(\.failedExitCode), [2, nil, nil])
    }

    /// #2148, red-first on 796a07307: the keep-when-empty rule keyed on
    /// `type == "gastown"`, so any other orchestrator's top-level row was
    /// pruned with its last rig. Any typed group is an orchestrator's.
    func testRemoveFromApiGroupsPreservesWorkerListGroupWhenEmpty() {
        let rigSession = makeSession(id: "rig-1")
        let team = AgentGroup(name: "team-a", agents: [rigSession])
        let pool = AgentGroup(name: "Build pool", type: "workerlist", agents: nil, groups: [team])
        sut.seedLocalApiGroups([pool])

        sut.removeFromApiGroups(sessionId: "rig-1")

        XCTAssertEqual(sut.apiGroups.count, 1,
                       "a worker-list top-level group must survive empty rigs")
        XCTAssertEqual(sut.apiGroups.first?.type, "workerlist")
        XCTAssertEqual(sut.apiGroups.first?.groups?.count, 0,
                       "empty team group must be pruned")
    }

    func testRemoveFromApiGroupsRecursesIntoNestedNonGasTownGroup() {
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

    func testRemoveFromApiGroupsDropsParentClearsOrphanedChildIds() {
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

    func testRemoveFromApiGroupsIsNoOpForUnknownId() {
        let original = makeSession(id: "alive", cost: 1.00)
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [original])])

        sut.removeFromApiGroups(sessionId: "ghost")

        XCTAssertEqual(sut.apiGroups.first?.agents?.map(\.id), ["alive"])
        XCTAssertEqual(sut.apiGroups.first?.agents?.first?.metrics?.estimatedCostUSD, 1.00)
        XCTAssertEqual(sut.groupedSessionIds, ["alive"])
    }

    // MARK: - deleteSession (regression for #287)

    func testDeleteSessionClearsBothMenuBarAndListViewSurfaces() {
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

    func testResetSessionStateFlipsBothSurfacesAndPreservesFields() throws {
        let id = UUID().uuidString
        let child = makeSession(id: "\(id)-child")
        let working = SessionState(
            id: id, state: .working, model: "test-model", cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: Date(timeIntervalSince1970: 0),
            updatedAt: Date(timeIntervalSince1970: 0),
            role: "test-role", children: [child]
        )
        let dir = sut.instancesPath
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

    // The instances directory is deliberately NOT re-derived here. It used to
    // be, from `homeDirectoryForCurrentUser`, and that is #1669: the fixture
    // landed in the live daemon's own directory, and the test could disagree
    // with the code under test about where that directory is. `sut.instancesPath`
    // is the one the subject reads, which under test is `AppHome`'s per-process
    // temporary home.

    // MARK: - SessionState.withChildren

    func testWithChildrenPreservesIdentityAndMetrics() {
        let child = makeSession(id: "child")
        let original = makeSession(id: "parent", cost: 4.20, children: nil)
        let replaced = original.withChildren([child])

        XCTAssertEqual(replaced.id, original.id)
        XCTAssertEqual(replaced.metrics?.estimatedCostUSD, 4.20)
        XCTAssertEqual(replaced.children?.map(\.id), ["child"])
    }

    func testWithChildrenAllowsClearingChildren() {
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
    func testRelaySameSessionIdDifferentDaemonsYieldsTwoRows() {
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
    func testRelaySameDaemonSameIdUpdatesInPlace() {
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha", state: "waiting"))

        XCTAssertEqual(sut.sessions.count, 1)
        XCTAssertEqual(sut.sessions.first?.state, .waiting)
    }

    /// A relay session whose id is also present locally collapses to the local
    /// row (local wins) — the v0 "same daemon over both paths shows once" guard.
    func testRelayIdAlsoPresentLocallyCollapsesToLocal() {
        sut.sessionMap["proc-1234"] = makeSession(id: "proc-1234")     // local, no daemonID
        sut.handleRelayMessage(relayPush(source: "daemonA", sessionId: "proc-1234", project: "alpha"))

        XCTAssertEqual(sut.sessions.filter { $0.id == "proc-1234" }.count, 1, "local wins; relay copy suppressed")
        XCTAssertNil(sut.sessions.first { $0.id == "proc-1234" }?.daemonID, "the surviving row is the local one")
    }

    // MARK: - Relay echo dedupe (#746)

    /// A project present locally that echoes back via the relay (under a drifted
    /// session_id, so it escapes the id-only filter) renders exactly once — the
    /// local copy wins; the relay group of the same name is suppressed.
    func testRelayEchoSameProjectNameDriftedIdCollapsesToLocal() {
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [makeSession(id: "local-1")])])
        // Relay echoes the same project under a ghost daemon + drifted id.
        sut.handleRelayMessage(relayPush(source: "ghostDaemon", sessionId: "drifted-9", project: "irrlicht"))

        let irrlicht = sut.apiGroups.filter { $0.name == "irrlicht" }
        XCTAssertEqual(irrlicht.count, 1, "a project present locally renders exactly once (local-wins)")
        XCTAssertTrue(
            irrlicht.first?.agents?.allSatisfy { $0.daemonID == nil } ?? false,
            "the surviving group is the local copy (no relay daemonID on its rows)"
        )
    }

    /// The same relay echo must also collapse in the flat `sessions` array
    /// (#828) — not just in `apiGroups`. Before the fix, `rebuildSessionsFromMap`
    /// only deduped relay sessions by id, so the drifted-id echo survived and
    /// the menu bar (which reads `sessions`) double-counted the project while
    /// the list view (which reads `apiGroups`) already showed the correct total.
    func testRelayEchoSameProjectNameDriftedIdCollapsesToLocalInFlatSessions() {
        sut.sessionMap["local-1"] = makeSession(id: "local-1", projectName: "irrlicht")
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [makeSession(id: "local-1", projectName: "irrlicht")])])
        sut.handleRelayMessage(relayPush(source: "ghostDaemon", sessionId: "drifted-9", project: "irrlicht"))

        let irrlicht = sut.sessions.filter { $0.projectName == "irrlicht" }
        XCTAssertEqual(irrlicht.count, 1, "the menu bar's flat session list must also collapse the relay echo")
        XCTAssertNil(irrlicht.first?.daemonID, "the surviving row is the local one")
    }

    /// A relay-only project with no local equivalent still appears — the
    /// name-collapse only suppresses collisions, not genuine remote projects.
    func testRelayOnlyDifferentProjectStillAppears() {
        sut.seedLocalApiGroups([AgentGroup(name: "irrlicht", agents: [makeSession(id: "local-1")])])
        sut.handleRelayMessage(relayPush(source: "daemonB", sessionId: "remote-1", project: "otherproj"))

        XCTAssertTrue(
            sut.apiGroups.contains { $0.name == "otherproj" },
            "a relay-only project with no local equivalent still appears"
        )
    }

    // MARK: - Origin glyph (#538)

    /// A relay session carries a daemonID, and the relay's snapshot label map
    /// resolves it to a hostname — the data the per-row origin glyph + tooltip
    /// render from. A local session has no daemonID (no glyph).
    func testOriginGlyphRemoteSessionResolvesHostnameTooltip() {
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
    func testOfflineDisconnectFadesRowsReconnectRestores() {
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

    // MARK: - Relayed session joins its worker's row (#2149)
    //
    // A session that runs in a container and reaches this Mac through the
    // relay is shown in its orchestrator worker's row. The join key is the
    // forwarding daemon's label (the container hostname), which equals the
    // worker's name. Red-first on 8d6279df7: six of the tests below fail
    // there. Four pass there by construction, because nothing joins: the two
    // `testLock…` tests, and the two whose comments name the rule they guard
    // against a mutation (the no-label daemon, two workers sharing a name).

    private static func slot(_ name: String, job: String? = nil) -> SessionManager.UnattachedWorker {
        SessionManager.UnattachedWorker(
            role: "worker", icon: "\u{1F527}", description: "Pool worker slot",
            workerName: name, workerID: job, workerState: job == nil ? "idle" : "busy"
        )
    }

    /// A worker-list pool whose rig holds free workers `team-a-m1-1` (busy on
    /// job-1) and `team-a-m1-2`, plus a local project group.
    private func seedPool(rigAgents: [SessionState]? = nil,
                          workers: [SessionManager.UnattachedWorker]? = nil,
                          extra: [AgentGroup] = []) {
        let rig = AgentGroup(name: "team-a", status: "operational", agents: rigAgents,
                             unattachedWorkers: workers ?? [Self.slot("team-a-m1-1", job: "job-1"), Self.slot("team-a-m1-2")])
        sut.seedLocalApiGroups([
            AgentGroup(name: "irrlicht", agents: [makeSession(id: "local-1", projectName: "irrlicht")]),
            AgentGroup(name: "Build pool", type: "workerlist", groups: [rig]),
        ] + extra)
    }

    private var rig: AgentGroup? {
        sut.apiGroups.first { $0.name == "Build pool" }?.groups?.first { $0.name == "team-a" }
    }

    private func group(_ name: String) -> AgentGroup? { sut.apiGroups.first { $0.name == name } }

    func testRelaySessionJoinsTheWorkerItsDaemonIsNamedAfter() {
        seedPool()
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))

        let joined = rig?.agents?.first { $0.rowID == "d1/proc-1" }
        XCTAssertNotNil(joined, "the relayed session is not in its worker's rig")
        XCTAssertEqual(joined?.workerName, "team-a-m1-1")
        XCTAssertEqual(joined?.workerID, "job-1")
        XCTAssertEqual(joined?.role, "worker")
        XCTAssertEqual(joined?.roleIcon, "\u{1F527}")
        XCTAssertEqual(joined?.daemonID, "d1", "the row keeps its relay origin (cloud glyph)")
        XCTAssertEqual(rig?.unattachedWorkers?.map(\.workerName), ["team-a-m1-2"],
                       "the joined worker still has a free-worker row")
        XCTAssertNil(group("container-proj"), "the joined session still has a project group")
        XCTAssertNil(sut.relaySessionMap["d1/proc-1"]?.workerName,
                     "the join wrote into the stored session")
        XCTAssertTrue(sut.sessions.contains { $0.rowID == "d1/proc-1" })
    }

    func testJoinedSessionLeavesTheWorkerWhenItsDaemonDisconnects() {
        seedPool()
        // A second, unrelated daemon stays connected, so the join still runs
        // after d1 leaves and has to tell a faded daemon from a connected one.
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1"), (id: "d2", label: "elsewhere")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))
        XCTAssertNotNil(rig?.agents?.first { $0.rowID == "d1/proc-1" }, "precondition: joined")

        sut.handleRelayMessage(RelayFixtures.daemonStatus(daemonID: "d1", status: "disconnected"))

        XCTAssertNil(rig?.agents?.first { $0.rowID == "d1/proc-1" }, "an offline daemon's session stayed joined")
        XCTAssertEqual(rig?.unattachedWorkers?.map(\.workerName), ["team-a-m1-1", "team-a-m1-2"])
        let faded = group("container-proj")?.agents?.first { $0.rowID == "d1/proc-1" }
        XCTAssertNotNil(faded, "the session did not return to its project group")
        XCTAssertTrue(faded.map { sut.isOffline($0) } ?? false, "the returned row is not faded")
        XCTAssertTrue(sut.sessions.contains { $0.rowID == "d1/proc-1" })
    }

    func testSnapshotAfterThePushJoinsWithNoFurtherPush() {
        seedPool()
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))
        XCTAssertNotNil(group("container-proj"), "precondition: unlabelled, it sits in its project group")

        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1")]))

        XCTAssertNotNil(rig?.agents?.first { $0.rowID == "d1/proc-1" }, "the snapshot did not recompose the groups")
        XCTAssertNil(group("container-proj"))
    }

    func testALabelChangeRebuildsTheMenuBarListToo() {
        // The join decides whether #746 hides a session from the flat
        // `sessions` the menu bar reads, so a label arriving or leaving has to
        // rebuild that list as well as the groups — in both directions.
        seedPool()
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d2", label: "elsewhere")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "irrlicht"))
        XCTAssertFalse(sut.sessions.contains { $0.rowID == "d1/proc-1" }, "precondition: #746 hides the unlabelled echo")

        sut.handleRelayMessage(RelayFixtures.daemonStatus(daemonID: "d1", status: "connected", label: "team-a-m1-1"))
        XCTAssertNotNil(rig?.agents?.first { $0.rowID == "d1/proc-1" })
        XCTAssertTrue(sut.sessions.contains { $0.rowID == "d1/proc-1" }, "a label arriving left the menu bar list stale")

        sut.handleRelayMessage(RelayFixtures.daemonStatus(daemonID: "d1", status: "disconnected"))
        XCTAssertFalse(sut.sessions.contains { $0.rowID == "d1/proc-1" }, "a label leaving left the menu bar list stale")
    }

    func testOfTwoDaemonsLabelledAlikeTheNewestSessionJoins() {
        seedPool()
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1"), (id: "d2", label: "team-a-m1-1")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d2", sessionId: "new", project: "container-proj", firstSeen: 200))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "old", project: "container-proj", firstSeen: 100))

        XCTAssertEqual(rig?.agents?.map(\.rowID), ["d2/new"])
        XCTAssertEqual(group("container-proj")?.agents?.map(\.rowID), ["d1/old"], "the older one keeps today's placement")
    }

    func testJoinWinsOverTheProjectNameCollapse() {
        // #746 hides a relay session whose project name a local group already
        // has — echoes of this Mac's own daemon, which carry this host's label
        // and never a worker's name. A joined session is exempt, in the list
        // and in the flat `sessions` the menu bar reads alike.
        seedPool()
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "irrlicht"))

        XCTAssertNotNil(rig?.agents?.first { $0.rowID == "d1/proc-1" })
        XCTAssertTrue(sut.sessions.contains { $0.rowID == "d1/proc-1" }, "the menu bar's list still collapses it")
        XCTAssertEqual(group("irrlicht")?.agents?.map(\.id), ["local-1"])
    }

    func testDaemonWithNoLabelIsNotNamedAfterItsId() {
        // The connect arm stores the id when a daemon sends no daemon_label;
        // the join must not read that as the daemon's name. Passes on the
        // parent by construction (nothing joins there); it guards the label
        // rule against the mutation that drops it.
        seedPool()
        sut.handleRelayMessage(RelayFixtures.daemonStatus(daemonID: "team-a-m1-1", status: "connected"))
        sut.handleRelayMessage(RelayFixtures.push(source: "team-a-m1-1", sessionId: "proc-1", project: "container-proj"))

        XCTAssertEqual(rig?.agents?.count ?? 0, 0, "a daemon id was taken for its label")
        XCTAssertNotNil(group("container-proj"))
    }

    func testLockNoMatchingWorkerKeepsTodaysPlacement() {
        seedPool()
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "nobody-here")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))

        XCTAssertEqual(group("container-proj")?.agents?.map(\.rowID), ["d1/proc-1"])
        XCTAssertEqual(rig?.unattachedWorkers?.count, 2)
    }

    func testLockAWorkerWithALocalSessionWins() {
        var local = makeSession(id: "local-x")
        local.role = "worker"
        local.workerName = "team-a-m1-1"
        seedPool(rigAgents: [local], workers: [Self.slot("team-a-m1-2")])
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))

        XCTAssertEqual(rig?.agents?.map(\.id), ["local-x"])
        XCTAssertEqual(group("container-proj")?.agents?.map(\.rowID), ["d1/proc-1"])
    }

    /// Passes on the parent by construction; guards the one-worker-per-name
    /// rule against the mutation that drops it.
    func testTwoWorkersWithTheNameJoinNothing() {
        let otherRig = AgentGroup(name: "rig", unattachedWorkers: [Self.slot("team-a-m1-1")])
        seedPool(extra: [AgentGroup(name: "gt", type: "gastown", groups: [otherRig])])
        sut.handleRelayMessage(RelayFixtures.snapshot(daemons: [(id: "d1", label: "team-a-m1-1")]))
        sut.handleRelayMessage(RelayFixtures.push(source: "d1", sessionId: "proc-1", project: "container-proj"))

        XCTAssertEqual(group("container-proj")?.agents?.map(\.rowID), ["d1/proc-1"])
        XCTAssertEqual(rig?.unattachedWorkers?.count, 2)
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

    /// Lifted to `RelayFixtures.push` so `SessionManagerGroupOrderTests` sends
    /// the same frame rather than seeding `relaySessionMap` by hand.
    private func relayPush(source: String, sessionId: String, project: String, state: String = "working") -> String {
        RelayFixtures.push(source: source, sessionId: sessionId, project: project, state: state)
    }

    private func makeSession(
        id: String,
        cost: Double = 0,
        children: [SessionState]? = nil,
        projectName: String? = nil
    ) -> SessionState {
        SessionState(
            id: id,
            state: .working,
            model: "test-model",
            cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            projectName: projectName,
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
