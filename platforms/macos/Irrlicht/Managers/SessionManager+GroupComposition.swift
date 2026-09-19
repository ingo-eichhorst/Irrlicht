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
    ///
    /// `providerKey` can now be the reserved `"unattributed"` bucket (issue
    /// #1996), for cost rows with no confirmed billing attribution,
    /// alongside real provider keys ("anthropic"/"openai"). This dictionary
    /// decode is already key-agnostic — Codable does not filter or
    /// whitelist keys — so no change was needed here to avoid dropping it.
    /// What is NOT true: `SessionListView.quotaUsageBody` (the one reader of
    /// `SessionManager.providerCosts`, populated from this field — see
    /// `SessionManager+Hydration.swift`) looks up a SPECIFIC provider key
    /// per quota chip, never enumerates this dictionary's keys generically,
    /// so nothing today renders an "Unattributed" chip from it. See #1996's
    /// report for the full audit.
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
    ///
    /// The ordering spans local AND relay rows (#1948): a project from a
    /// second daemon is a row the user sees, so it is a row the user can move.
    /// Ordering only the local half was what made a relay group's chevrons
    /// inert — it was rendered but absent from `projectGroupOrder`, the list
    /// `moveProjectGroupUp/Down` act on.
    ///
    /// `groupedSessionIds` stays local-only: it feeds `patchApiGroups`' guard,
    /// which is about the LOCAL patch path, not about display.
    func recomposeApiGroups() {
        let localNames = Set(localApiGroups.map(\.name))
        let displayed = localApiGroups
            + relayGroups().filter { !localNames.contains($0.name) }
        apiGroups = orderedGroups(displayed)
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

    /// Deduped on the way in — this is the one point where persisted,
    /// user-editable, migration-aged data becomes `projectGroupOrder`, so the
    /// in-memory array holds the no-duplicates invariant from birth rather
    /// than acquiring it at the first recompose. Between this call and that
    /// recompose, `reorderMoves(for:)` answers from whatever is here.
    func loadProjectGroupOrder() {
        var seen = Set<String>()
        projectGroupOrder = (defaults.stringArray(forKey: projectGroupOrderKey) ?? [])
            .filter { seen.insert($0).inserted }
        print("📋 Loaded project group order with \(projectGroupOrder.count) groups")
    }

    func saveProjectGroupOrder() {
        defaults.set(projectGroupOrder, forKey: projectGroupOrderKey)
        print("💾 Saved project group order with \(projectGroupOrder.count) groups")
    }

    /// Appends any first-seen names to the REMEMBERED order and returns the
    /// groups sorted by it.
    ///
    /// `projectGroupOrder` is remembered order, not rendered order (#1954). It
    /// only ever grows, by appending a name the first time that name is
    /// rendered; it is never pruned because a payload lacked a name, and an
    /// empty payload writes nothing at all. A name's absence carries no
    /// information about where the user wants it: the payloads a relay project
    /// goes missing from are ordinary churn — a `session_deleted`, a relay
    /// disconnect, `restoreDaemon`, a recompose before the first hydration
    /// lands — and #1954's log shows those arriving seconds apart. Pruning
    /// turned each of them into a rewrite of the user's order, re-appending
    /// the name LAST when it came back. #1949 is what made that reach relay
    /// rows, by putting them in this array at all.
    ///
    /// What consumes the difference: `renderedGroupOrder` narrows this to what
    /// is on screen, and that projection — not this array — is the reorderable
    /// domain.
    ///
    /// The other consumer is the menu bar icon, which takes this array
    /// verbatim (`MenuBarImageBuilder` → `MenuBarStatusRenderer
    /// .orderedProjectGroups`). It walks it `removeValue(forKey:)`-ing from a
    /// session-derived map and skips misses, so a remembered-but-absent name
    /// costs it nothing and it needed no change here. That was READ, and then
    /// run: no test passed it a name its sessions did not mention until
    /// `MenuBarStatusRendererTests
    /// .testARememberedNameWithNoSessionsDrawsNothingAndDoesNotDisplaceTheRest`,
    /// added with this change, which pins the icon byte-identical under a
    /// superset order.
    func orderedGroups(_ groups: [AgentGroup]) -> [AgentGroup] {
        // `seen` starts from what is already remembered, so this both skips
        // known names and enforces "once" among the incoming ones: two groups
        // can share a name, and a duplicate would inflate the count
        // `reorderMoves(for:)` reports and give the sort below two entries to
        // choose between.
        var seen = Set(projectGroupOrder)
        let firstSeen = groups.map(\.name).filter { seen.insert($0).inserted }

        // The only write on this path. No first-seen name means nothing to
        // remember — which is every empty payload, and every recompose that
        // merely lost a name.
        if !firstSeen.isEmpty {
            projectGroupOrder += firstSeen
            saveProjectGroupOrder()
        }

        // Built by hand rather than with `Dictionary(uniqueKeysWithValues:)`,
        // which TRAPS on a repeated key — a crash, not a mis-sort. The filter
        // above already rules that out, but this array is also written by
        // `loadProjectGroupOrder` from a user-editable store, and a reader
        // that crashes on bad input is the wrong response for a menu bar app.
        // First occurrence wins, matching `firstIndex(of:)` below.
        var index: [String: Int] = [:]
        for (offset, name) in projectGroupOrder.enumerated() where index[name] == nil {
            index[name] = offset
        }
        return groups.sorted { (index[$0.name] ?? Int.max) < (index[$1.name] ?? Int.max) }
    }

    /// The remembered order narrowed to what is on screen: the rendered list's
    /// names, in the sequence the user chose. This — not `projectGroupOrder` —
    /// is the reorderable domain, and it is computed rather than persisted.
    ///
    /// Both halves of the chevron contract measure against it, which is what
    /// keeps #1948 from returning in a new costume: a remembered-but-absent
    /// name padding the `count - 1` bound would give the last VISIBLE row a
    /// live down-chevron aimed at something nobody can see.
    ///
    /// Two mutations were RUN through `tools/mutate.sh` against
    /// `swift test --filter SessionManagerGroupOrderTests`, and both land on
    /// `testChevronOffersAgreeWithWhatTheHandlersDo`'s absent-name arm:
    ///
    /// - taking the bound from `projectGroupOrder` inside
    ///   `reorderMoves(atIndex:in:)` — so the offer AND the handler use it —
    ///   traps: `Swift/ContiguousArrayBuffer.swift:692: Fatal error: Index out
    ///   of range`, because the handler then indexes past the projection it
    ///   swaps within. That trap is why `domain` is a parameter here rather
    ///   than read again inside: the index and the bound cannot come from two
    ///   different arrays;
    /// - taking it only in `reorderMoves(for:)`, leaving the handlers on the
    ///   projection, fails with `XCTAssertEqual failed: ("true") is not equal
    ///   to ("false") - the down-chevron offer for gamma disagrees with the
    ///   handler` — #1948's exact shape.
    ///
    /// Both arms of that test pass unmutated; only the absent-name arm goes
    /// red, which is what makes that arm worth its ink.
    var renderedGroupOrder: [String] {
        let rendered = Set(apiGroups.map(\.name))
        return projectGroupOrder.filter { rendered.contains($0) }
    }

    /// Which way `name` can be moved, or `nil` when it is not reorderable at
    /// all — the only question the reorder chevrons ask.
    ///
    /// Answers about `renderedGroupOrder`. #1948 is what happens when the offer
    /// and the effect measure against different lists: back then the order
    /// covered only the LOCAL half of the rendered list, so a relay row padded
    /// the count the view saw — the last local group's down-chevron rendered
    /// enabled and did nothing, and the relay group got two chevrons that could
    /// never fire. `nil` means a name that is not a rendered top-level group.
    ///
    /// Booleans, not coordinates, so the offer and the effect evaluate the
    /// same expression: `moveProjectGroupUp/Down` guard on these, and
    /// `GroupView` renders from them. Index arithmetic lives here and nowhere
    /// else. `SessionManagerGroupOrderTests` asserts the two agree for every
    /// rendered group, in both the all-rendered case and the case where the
    /// remembered order carries a name that has left.
    func reorderMoves(for name: String) -> (up: Bool, down: Bool)? {
        let domain = renderedGroupOrder
        return domain.firstIndex(of: name).map { reorderMoves(atIndex: $0, in: domain) }
    }

    /// The rule itself, written once. Each handler judges the same index it
    /// then swaps — asking `reorderMoves(for:)` and looking the index up
    /// separately would be two lookups again, and a disagreement between them
    /// swaps out of bounds instead of declining. `domain` is passed in so the
    /// index and the bound can never come from two different arrays.
    private func reorderMoves(atIndex i: Int, in domain: [String]) -> (up: Bool, down: Bool) {
        (up: i > 0, down: i < domain.count - 1)
    }

    func moveProjectGroupUp(name: String) { moveProjectGroup(name: name, by: -1) }

    func moveProjectGroupDown(name: String) { moveProjectGroup(name: name, by: +1) }

    /// One mover for both chevrons: they differ only in which boolean gates
    /// them and which neighbour they swap with, and a second copy of the
    /// index-and-swap dance is how the offer and the effect drift apart — #1948.
    /// `offset` is a step in the RENDERED list, ±1.
    ///
    /// The index and the bound both come from the same local `domain`, so they
    /// cannot disagree, and `domain[i + offset]` is reached only behind the
    /// matching boolean.
    ///
    /// The swap is BY NAME, not by rendered index: the pair is adjacent on
    /// screen but need not be adjacent in `projectGroupOrder`, where
    /// remembered-but-absent names sit between them and keep their own index —
    /// that is the whole point of remembering them. Consequence, chosen rather
    /// than stumbled into: an absent name keeps its ABSOLUTE slot, so which
    /// visible neighbours it returns between can change when they move around
    /// it.
    private func moveProjectGroup(name: String, by offset: Int) {
        let domain = renderedGroupOrder
        guard let i = domain.firstIndex(of: name) else { return }
        let moves = reorderMoves(atIndex: i, in: domain)
        guard offset < 0 ? moves.up : moves.down else { return }

        let neighbour = domain[i + offset]
        guard let ia = projectGroupOrder.firstIndex(of: name),
              let ib = projectGroupOrder.firstIndex(of: neighbour) else {
            // Not reachable while `renderedGroupOrder` stays a FILTER of
            // `projectGroupOrder`: every name in the domain came out of it.
            // Checked rather than asserted — `git grep -n "apiGroups = "
            // platforms/macos/Irrlicht` finds one assignment, in
            // `recomposeApiGroups`, and it goes through `orderedGroups`, which
            // appends every first-seen name before it sorts.
            // Logged rather than trapped: a chevron that silently does nothing
            // is the harder failure to diagnose in a menu bar app. Same idiom
            // as `patchApiGroups`' dropped-id warning above.
            if isDebugMode {
                print("⚠️ moveProjectGroup: \(name) or \(neighbour) is rendered but not remembered — declining")
            }
            return
        }
        projectGroupOrder.swapAt(ia, ib)
        saveProjectGroupOrder()
        recomposeApiGroups()
    }
}
