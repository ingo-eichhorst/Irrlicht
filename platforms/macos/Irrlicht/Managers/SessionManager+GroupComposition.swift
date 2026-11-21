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
        // `var` so `patchGroup` / `pruneGroup` copy the group and replace only
        // these two: every other field then survives a rebuild by construction.
        var agents: [SessionState]?
        var groups: [AgentGroup]?
        /// Trailing-window cost totals (USD) keyed by timeframe string
        /// ("day", "week", "month", "year"). Present on non-orchestrator
        /// top-level groups.
        let costs: [String: Double]?
        /// Orchestrator workers in this group that no session is attached to
        /// (#2140): a free polecat, an idle pool slot. Not sessions — they
        /// never enter `sessionMap`, the menu bar or notifications. `var` so
        /// the displayed copy can drop a worker a relayed session joined
        /// (#2149, `placingRelayJoins`).
        var unattachedWorkers: [UnattachedWorker]?

        var id: String { name }
        /// Gas Town's own group — for its branding only (the ⛽ glyph, the
        /// tooltip, the menu-bar badge). Structure keys on `isOrchestrator`.
        var isGasTown: Bool { type == "gastown" }
        /// Any orchestrator's top-level group — Gas Town or a worker list
        /// (#2148): the daemon sets `type` on those and on nothing else.
        var isOrchestrator: Bool { !(type ?? "").isEmpty }

        // Explicit because the wire key is snake_case and the decoder in
        // SessionManager+Hydration.swift is a plain `JSONDecoder()`.
        enum CodingKeys: String, CodingKey {
            case name, type, status, agents, groups, costs
            case unattachedWorkers = "unattached_workers"
        }

        init(name: String, type: String? = nil, status: String? = nil, agents: [SessionState]? = nil, groups: [AgentGroup]? = nil, costs: [String: Double]? = nil, unattachedWorkers: [UnattachedWorker]? = nil) {
            self.name = name
            self.type = type
            self.status = status
            self.agents = agents
            self.groups = groups
            self.costs = costs
            self.unattachedWorkers = unattachedWorkers
        }
    }

    /// One `unattached_workers` entry: an orchestrator worker with no
    /// session. `workerState` is the orchestrator's own state string, shown
    /// as text — never mapped onto `SessionState.State`. The one value read
    /// as more than text is a worker-list slot's "waiting" (#2145): see
    /// `isWaiting`.
    struct UnattachedWorker: Decodable, Equatable, Identifiable {
        let role: String?
        let icon: String?
        let description: String?
        let workerName: String
        let workerID: String?
        let workerState: String?
        /// Row-only indications (#2148): the busy slot stopped reporting, and
        /// the exit code of its last completed run. Both omitted by the daemon
        /// when unset; `var` with a nil default so a payload or a test fixture
        /// without them still builds.
        var stale: Bool? = nil
        var lastExit: Int? = nil

        /// The daemon lists a name at most once per group.
        var id: String { workerName }

        var isStale: Bool { stale == true }
        /// The worker's state reads "waiting" (#2145). Only the worker list
        /// sends it as a contract, so the row paint
        /// (`UnattachedWorkerRowView.paintsWaiting`) and the notification edge
        /// (`waitingWorkerEdges`) both act on it in worker-list groups only.
        var isWaiting: Bool { workerState == "waiting" }
        /// The last run's exit code when it failed (non-zero), else nil — an
        /// exit of 0 and no completed run both show nothing.
        var failedExitCode: Int? {
            guard let code = lastExit, code != 0 else { return nil }
            return code
        }

        enum CodingKeys: String, CodingKey {
            case role, icon, description, stale
            case workerName = "worker_name"
            case workerID = "worker_id"
            case workerState = "worker_state"
            case lastExit = "last_exit"
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
    /// A relayed session joined into an orchestrator worker's row (#2149,
    /// `relayWorkerJoins`) is shown in that worker's group instead of a relay
    /// project group, so the #746 collapse never reaches it.
    ///
    /// `groupedSessionIds` stays local-only: it feeds `patchApiGroups`' guard,
    /// which is about the LOCAL patch path, not about display.
    func recomposeApiGroups() {
        let joins = relayWorkerJoins()
        let joinedRowIDs = Set(joins.values.map(\.rowID))
        let localNames = Set(localApiGroups.map(\.name))
        let displayed = localApiGroups.map { placingRelayJoins(joins, in: $0) }
            + relayGroups(excluding: joinedRowIDs).filter { !localNames.contains($0.name) }
        apiGroups = orderedGroups(displayed)
        groupedSessionIds = Set(localApiGroups.flatMap { collectSessionIds(from: $0) })
    }

    /// Relayed sessions shown in an orchestrator worker's row (#2149), keyed by
    /// worker name, each a copy carrying that worker's role, icon,
    /// description, name and work item. A session that runs in a container
    /// and reaches this Mac through the relay joins worker X only when
    /// - its daemon is connected and named X: read from `relayDaemons`, which
    ///   holds connected daemons only — never `daemonLabels` or
    ///   `daemonLabel(for:)`, which include faded daemons and fall back to the
    ///   id. A label equal to the daemon id is the snapshot / `daemon_status`
    ///   arms' stand-in for a missing `daemon_label` and never joins;
    /// - it is a relay root (no parent) with no local copy by id — the same
    ///   id-only filter as `relayGroups(excluding:)`;
    /// - exactly one worker in the local groups is named X, counting session
    ///   rows with that `workerName` and `unattachedWorkers` entries alike,
    ///   and that one is unattached. A worker with a local session therefore
    ///   wins.
    /// Of several candidates the newest `firstSeen` joins, a tie goes to the
    /// lower `rowID`, and the rest keep today's placement. Derived on every
    /// recompose and never written into `localApiGroups` or `relaySessionMap`.
    func relayWorkerJoins() -> [String: SessionState] {
        guard !relaySessionMap.isEmpty, !relayDaemons.isEmpty else { return [:] }
        let free = uniquelyNamedFreeWorkers()
        guard !free.isEmpty else { return [:] }
        let localIDs = Set(sessionMap.keys)
        var winners: [String: SessionState] = [:]
        for s in relaySessionMap.values where s.parentSessionId == nil && !localIDs.contains(s.id) {
            guard let name = connectedDaemonName(of: s), let w = free[name] else { continue }
            if let held = winners[name], !s.joinsAhead(of: held) { continue }
            winners[name] = s.decorated(as: w)
        }
        return winners
    }

    /// The unattached workers whose name no other worker in the local groups
    /// carries, by name. Session rows are counted over `flattenAgents`, the
    /// walk that fills `sessionMap`, so a name busy in one rig and free in
    /// another is not unique.
    func uniquelyNamedFreeWorkers() -> [String: UnattachedWorker] {
        var count: [String: Int] = [:]
        for s in localApiGroups.flatMap({ flattenAgents(from: $0) }) {
            if let name = s.workerName { count[name, default: 0] += 1 }
        }
        var free: [String: UnattachedWorker] = [:]
        func collect(_ group: AgentGroup) {
            for w in group.unattachedWorkers ?? [] {
                count[w.workerName, default: 0] += 1
                free[w.workerName] = w
            }
            group.groups?.forEach(collect)
        }
        localApiGroups.forEach(collect)
        return free.filter { count[$0.key] == 1 }
    }

    /// The name the session's daemon gave itself, while that daemon is
    /// connected; nil for a local session, a faded daemon, or a daemon whose
    /// stored label is its own id (the stand-in for a missing `daemon_label`).
    func connectedDaemonName(of session: SessionState) -> String? {
        guard let daemon = session.daemonID, let name = relayDaemons[daemon], name != daemon else { return nil }
        return name
    }

    /// `group` as displayed: each joined session appended to the session rows
    /// of the group that lists its worker, and that worker dropped from the
    /// group's `unattachedWorkers` (#2149). A worker's name is unique in the
    /// tree whenever it joined, so matching by name finds the one group.
    func placingRelayJoins(_ joins: [String: SessionState], in group: AgentGroup) -> AgentGroup {
        guard !joins.isEmpty else { return group }
        var placed = group
        let joinedHere = (group.unattachedWorkers ?? []).compactMap { joins[$0.workerName] }
        if !joinedHere.isEmpty {
            placed.agents = (group.agents ?? []) + joinedHere
            placed.unattachedWorkers = group.unattachedWorkers?.filter { joins[$0.workerName] == nil }
        }
        placed.groups = group.groups?.map { placingRelayJoins(joins, in: $0) }
        return placed
    }

    /// Test seam: install local (non-relay) groups through the same path the
    /// hydration uses. Setting the published `apiGroups` directly is not
    /// enough — `patchApiGroups` / `removeFromApiGroups` operate on
    /// `localApiGroups` and recompose over it.
    func seedLocalApiGroups(_ groups: [AgentGroup]) {
        installLocalGroups(groups)
    }

    /// Installs the local daemon's groups and recomposes the published
    /// surfaces. Every hydrate goes through here, so a worker-list slot's
    /// edge into waiting (#2145) is diffed against exactly the groups being
    /// replaced. `hydrateSessions` installs a response only when it is newer
    /// than the last one installed, so a late older response cannot replay
    /// an edge. A worker joined by a relayed session (#2149) shows that
    /// session, which notifies for itself, so its edge is dropped.
    func installLocalGroups(_ groups: [AgentGroup]) {
        let edges = Self.waitingWorkerEdges(from: localApiGroups, to: groups)
        localApiGroups = groups
        recomposeApiGroups()
        let joined = relayWorkerJoins()
        notifyWaitingWorkers(edges.filter { joined[$0.worker] == nil })
    }

    /// Groups relay-only sessions (ids not present locally) by project name,
    /// leaving out the `excluding` rowIDs — sessions joined into a worker's
    /// row (#2149). No child nesting in v0; the common same-daemon case yields
    /// no relay-only rows, so this only renders a genuine second daemon's
    /// sessions.
    func relayGroups(excluding joined: Set<String>) -> [AgentGroup] {
        guard !relaySessionMap.isEmpty else { return [] }
        let localIDs = Set(sessionMap.keys)
        let relayOnly = relaySessionMap.values.filter { !localIDs.contains($0.id) && !joined.contains($0.rowID) }
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
        var patched = group
        patched.agents = patchedAgents
        if hasNestedMatch { patched.groups = group.groups?.map { patchGroup($0, with: session) } }
        return patched
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

    /// Returns `nil` when the group has nothing left to render — except an
    /// orchestrator's group (Gas Town, a worker list), which keeps its
    /// top-level row even with no rigs. Free
    /// orchestrator workers (#2140) count as something to render, so a rig
    /// whose last session left keeps its row while it still lists them.
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
            && (group.unattachedWorkers?.isEmpty ?? true)
        if isEmpty && !group.isOrchestrator { return nil }

        var pruned = group
        pruned.agents = prunedAgents
        pruned.groups = prunedGroups
        return pruned
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

// The relay-worker join's tie-break and decoration (#2149).
fileprivate extension SessionState {
    /// Joins ahead of `other` for the same worker: started later, or in the
    /// same second with the lower `rowID`.
    func joinsAhead(of other: SessionState) -> Bool {
        if firstSeen != other.firstSeen { return firstSeen > other.firstSeen }
        return rowID < other.rowID
    }

    /// A copy shown as worker `w`: its role, icon, description, name and work
    /// item; everything else stays the session's own.
    func decorated(as w: SessionManager.UnattachedWorker) -> SessionState {
        var copy = self
        copy.role = w.role
        copy.roleIcon = w.icon
        copy.roleDescription = w.description
        copy.workerName = w.workerName
        copy.workerID = w.workerID
        return copy
    }
}
