import Foundation

// MARK: - apiGroups composition (local + relay) and ordering
//
// Split out of SessionManager.swift (#807): the recursive group tree the
// published `apiGroups` is built from — merging local + relay sources,
// patching/pruning individual sessions in place, and persisting the user's
// project ordering.

extension SessionManager {
    /// Top-level /api/v1/sessions payload: the dashboard hierarchy plus
    /// per-provider trailing-window spend. `provider_costs` is keyed
    /// providerKey → timeframe ("day"/"week"/"month"/"year") → USD.
    struct SessionsResponse: Decodable {
        let groups: [AgentGroup]
        let providerCosts: [String: [String: Double]]?

        enum CodingKeys: String, CodingKey {
            case groups
            case providerCosts = "provider_costs"
        }
    }

    /// Recursive group structure from the sessions API.
    struct AgentGroup: Decodable, Identifiable {
        let name: String
        let type: String?
        let status: String?
        let agents: [SessionState]?
        let groups: [AgentGroup]?
        /// Trailing-window cost totals (USD) keyed by timeframe string
        /// ("day", "week", "month", "year"). Present on non-orchestrator
        /// top-level groups.
        let costs: [String: Double]?

        var id: String { name }
        var isGasTown: Bool { type == "gastown" }

        init(name: String, type: String? = nil, status: String? = nil, agents: [SessionState]? = nil, groups: [AgentGroup]? = nil, costs: [String: Double]? = nil) {
            self.name = name
            self.type = type
            self.status = status
            self.agents = agents
            self.groups = groups
            self.costs = costs
        }
    }

    /// Rebuilds the published `apiGroups` from the local groups plus
    /// client-side groups for relay-only sessions, and refreshes
    /// `groupedSessionIds` (used by the local patch guard) from the local set.
    ///
    /// Local-wins name-collapse (#746): a relay group whose project name already
    /// exists locally is dropped, so a project the local daemon publishes — then
    /// echoes back via the relay it publishes to — renders once. The collapse
    /// keys on project *name*, not session_id: the echoed/ghost relay rows carry
    /// drifted ids that escape `relayGroups()`'s id-only filter. Relay-only
    /// projects (no local group of that name) still appear.
    func recomposeApiGroups() {
        let localNames = Set(localApiGroups.map(\.name))
        apiGroups = orderedGroups(localApiGroups)
            + relayGroups().filter { !localNames.contains($0.name) }
        groupedSessionIds = Set(localApiGroups.flatMap { collectSessionIds(from: $0) })
    }

    /// Test seam: install local (non-relay) groups and recompose the published
    /// surfaces, mirroring how the hydration path installs groups. Setting the
    /// published `apiGroups` directly is not enough — `patchApiGroups` /
    /// `removeFromApiGroups` operate on `localApiGroups` and recompose over it.
    func seedLocalApiGroups(_ groups: [AgentGroup]) {
        localApiGroups = groups
        recomposeApiGroups()
    }

    /// Groups relay-only sessions (ids not present locally) by project name.
    /// No orchestrator handling or child nesting in v0; the common same-daemon
    /// case yields no relay-only rows, so this only renders a genuine second
    /// daemon's sessions.
    func relayGroups() -> [AgentGroup] {
        guard !relaySessionMap.isEmpty else { return [] }
        let localIDs = Set(sessionMap.keys)
        let relayOnly = relaySessionMap.values.filter { !localIDs.contains($0.id) }
        // A relay session is top-level only if its parent is unknown within its
        // own daemon (rowIDs are "daemon/id") and is not a local session —
        // matching rebuildSessionsFromMap. Keeps a relay child from surfacing as
        // a stray top-level row, while two daemons sharing a session_id stay
        // distinct (#537).
        let relayParentKeys = Set(relaySessionMap.keys)
        let topLevel = relayOnly.filter { s in
            guard let pid = s.parentSessionId else { return true }
            if let dID = s.daemonID {
                return !relayParentKeys.contains("\(dID)/\(pid)")
            }
            return !localIDs.contains(pid)
        }
        guard !topLevel.isEmpty else { return [] }
        var byProject: [String: [SessionState]] = [:]
        var order: [String] = []
        for s in topLevel.sorted(by: { $0.rowID < $1.rowID }) {
            let key = s.projectName ?? "unknown"
            if byProject[key] == nil { order.append(key) }
            byProject[key, default: []].append(s)
        }
        return order.map { AgentGroup(name: $0, agents: byProject[$0]) }
    }

    func collectSessionIds(from group: AgentGroup) -> [String] {
        let direct = (group.agents ?? []).map(\.id)
        let children = (group.agents ?? []).flatMap { $0.children?.map(\.id) ?? [] }
        let nested = (group.groups ?? []).flatMap { collectSessionIds(from: $0) }
        return direct + children + nested
    }

    /// Recursively flatten agents from a group and its sub-groups.
    func flattenAgents(from group: AgentGroup) -> [SessionState] {
        var result: [SessionState] = []
        for agent in group.agents ?? [] {
            result.append(agent)
            for child in agent.children ?? [] {
                result.append(child)
            }
        }
        for subGroup in group.groups ?? [] {
            result += flattenAgents(from: subGroup)
        }
        return result
    }

    /// Patch a session in-place within apiGroups so the list view updates reactively.
    /// On miss, schedule a debounced rehydration to self-heal — this closes the race
    /// where a newly created session's first WS updates arrive before it's been
    /// registered in `groupedSessionIds`.
    func patchApiGroups(session: SessionState) {
        guard groupedSessionIds.contains(session.id) else {
            if isDebugMode {
                let known = sessionMap[session.id] != nil
                print("⚠️ patchApiGroups guard dropped id=\(session.id) inSessionMap=\(known) — scheduling rehydration")
            }
            scheduleRehydration()
            return
        }
        localApiGroups = localApiGroups.map { patchGroup($0, with: session) }
        recomposeApiGroups()
    }

    func patchGroup(_ group: AgentGroup, with session: SessionState) -> AgentGroup {
        let hasAgentMatch = group.agents?.contains { $0.id == session.id } ?? false
        let hasChildMatch = group.agents?.contains { agent in
            agent.children?.contains { $0.id == session.id } ?? false
        } ?? false
        let hasNestedMatch = group.groups?.contains { groupContains($0, sessionId: session.id) } ?? false
        guard hasAgentMatch || hasChildMatch || hasNestedMatch else { return group }

        let patchedAgents: [SessionState]? = (hasAgentMatch || hasChildMatch)
            ? group.agents?.map { agent in
                if agent.id == session.id {
                    // Preserve children when patching a parent whose id matches.
                    return session.withChildren(agent.children)
                }
                if let kids = agent.children, kids.contains(where: { $0.id == session.id }) {
                    let patchedKids = kids.map { $0.id == session.id ? session : $0 }
                    return agent.withChildren(patchedKids)
                }
                return agent
            }
            : group.agents
        let patchedGroups = hasNestedMatch ? group.groups?.map { patchGroup($0, with: session) } : group.groups
        return AgentGroup(
            name: group.name,
            type: group.type,
            status: group.status,
            agents: patchedAgents,
            groups: patchedGroups,
            costs: group.costs
        )
    }

    func groupContains(_ group: AgentGroup, sessionId: String) -> Bool {
        if group.agents?.contains(where: { $0.id == sessionId }) == true { return true }
        if group.agents?.contains(where: { ($0.children ?? []).contains(where: { $0.id == sessionId }) }) == true { return true }
        return group.groups?.contains { groupContains($0, sessionId: sessionId) } ?? false
    }

    /// Synchronously drop a session from `apiGroups` on `session_deleted` —
    /// the debounced rehydrate is a safety net, not the primary path, so the
    /// overlay can't render a stale row while the menu bar is already idle.
    func removeFromApiGroups(sessionId: String) {
        guard groupedSessionIds.contains(sessionId) else { return }
        // Prune the local groups, then recompose. Rebuilding groupedSessionIds
        // (inside recompose) rather than `remove(sessionId)` matters because
        // pruning a parent or nested group transitively orphans embedded ids
        // that must also leave the set, else `patchApiGroups` passes its guard
        // for sessions that no longer have a row.
        localApiGroups = localApiGroups.compactMap { pruneGroup($0, removing: sessionId) }
        recomposeApiGroups()
    }

    /// Returns `nil` when the group has nothing left to render — except
    /// gas-town, which keeps its top-level row even with no rigs.
    func pruneGroup(_ group: AgentGroup, removing sessionId: String) -> AgentGroup? {
        let prunedAgents: [SessionState]? = group.agents?.compactMap { agent in
            if agent.id == sessionId { return nil }
            if let kids = agent.children, kids.contains(where: { $0.id == sessionId }) {
                let filtered = kids.filter { $0.id != sessionId }
                return agent.withChildren(filtered.isEmpty ? nil : filtered)
            }
            return agent
        }
        let prunedGroups: [AgentGroup]? = group.groups?.compactMap { pruneGroup($0, removing: sessionId) }

        let isEmpty = (prunedAgents?.isEmpty ?? true) && (prunedGroups?.isEmpty ?? true)
        if isEmpty && !group.isGasTown { return nil }

        return AgentGroup(
            name: group.name,
            type: group.type,
            status: group.status,
            agents: prunedAgents,
            groups: prunedGroups,
            costs: group.costs
        )
    }

    // MARK: - Project Group Order Management

    func loadProjectGroupOrder() {
        projectGroupOrder = UserDefaults.standard.stringArray(forKey: projectGroupOrderKey) ?? []
        print("📋 Loaded project group order with \(projectGroupOrder.count) groups")
    }

    func saveProjectGroupOrder() {
        UserDefaults.standard.set(projectGroupOrder, forKey: projectGroupOrderKey)
        print("💾 Saved project group order with \(projectGroupOrder.count) groups")
    }

    /// Syncs `projectGroupOrder` with incoming names (appends new, drops gone)
    /// and returns the groups sorted by that order.
    func orderedGroups(_ groups: [AgentGroup]) -> [AgentGroup] {
        let incomingNames = groups.map(\.name)
        let currentNames = Set(incomingNames)
        let known = Set(projectGroupOrder)

        var updated = projectGroupOrder.filter { currentNames.contains($0) }
        let newNames = incomingNames.filter { !known.contains($0) }
        updated.append(contentsOf: newNames)

        if updated != projectGroupOrder {
            projectGroupOrder = updated
            saveProjectGroupOrder()
        }

        let index = Dictionary(uniqueKeysWithValues: projectGroupOrder.enumerated().map { ($1, $0) })
        return groups.sorted { (index[$0.name] ?? Int.max) < (index[$1.name] ?? Int.max) }
    }

    func moveProjectGroupUp(name: String) {
        guard let i = projectGroupOrder.firstIndex(of: name), i > 0 else { return }
        projectGroupOrder.swapAt(i, i - 1)
        saveProjectGroupOrder()
        recomposeApiGroups()
    }

    func moveProjectGroupDown(name: String) {
        guard let i = projectGroupOrder.firstIndex(of: name),
              i < projectGroupOrder.count - 1 else { return }
        projectGroupOrder.swapAt(i, i + 1)
        saveProjectGroupOrder()
        recomposeApiGroups()
    }
}
