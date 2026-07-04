import Foundation

// MARK: - REST hydration, consent, and backchannel actions
//
// Split out of SessionManager.swift (#807): everything that talks to the
// local daemon over plain HTTP rather than the WebSocket — the periodic full
// snapshot, agent branding, permission consent, and the backchannel input/
// interrupt actions.

extension SessionManager {
    /// Agents the auto wizard should show: detected, with at least one
    /// pending permission, in ask mode. Empty in grant-all mode (wizard
    /// suppressed for demo/record/test daemons).
    var pendingWizardAgents: [AgentPermissions] {
        guard let snap = permissionsSnapshot, snap.mode == "ask" else { return [] }
        return snap.agents.filter(\.needsWizard)
    }

    /// Fetches the daemon's adapter branding registry into `AgentRegistry.byName`.
    /// Called once per (re)connect from `connect()` — there's no periodic
    /// refresh because adapter rollouts require a daemon restart, which
    /// drops the WebSocket and triggers a reconnect anyway, which calls us.
    /// If a future change ships hot-loadable adapters, add a refresh hook
    /// (or push the registry over the WebSocket).
    func hydrateAgents() async {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/agents") else { return }
        do {
            let (data, response) = try await localURLSession.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            let entries = try JSONDecoder().decode([AgentBranding].self, from: data)
            AgentRegistry.byName = Dictionary(uniqueKeysWithValues: entries.map { ($0.name, $0) })
            print("💧 Hydrated \(entries.count) agent brandings from REST API")
        } catch {
            print("💧 Agent branding hydration failed: \(error.localizedDescription)")
        }
    }

    /// Fetches the consent state from the local daemon. Older daemons
    /// (pre-#570) have no /api/v1/permissions — the snapshot stays nil and
    /// no wizard ever appears.
    func refreshPermissions() async {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/permissions") else { return }
        do {
            let (data, response) = try await localURLSession.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            permissionsSnapshot = try JSONDecoder().decode(PermissionsSnapshot.self, from: data)
        } catch {
            print("🔐 Permissions refresh failed: \(error.localizedDescription)")
        }
    }

    /// Submits wizard/Settings decisions. The daemon applies effects
    /// (installs/uninstalls hooks, starts/stops watchers), persists, and
    /// broadcasts `permissions_updated` so the web dashboard dismisses its
    /// wizard — first answer wins. The response body is the updated
    /// snapshot, applied immediately so the UI doesn't wait on the push.
    /// Returns false on failure so the wizard can stay up for a retry
    /// instead of silently losing the user's consent decisions.
    @discardableResult
    func answerPermissions(_ answers: [PermissionAnswer]) async -> Bool {
        guard !answers.isEmpty,
              let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/permissions/answer") else { return false }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        do {
            request.httpBody = try JSONEncoder().encode(["answers": answers])
            let (data, response) = try await localURLSession.data(for: request)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return false }
            permissionsSnapshot = try JSONDecoder().decode(PermissionsSnapshot.self, from: data)
            return true
        } catch {
            print("🔐 Permissions answer failed: \(error.localizedDescription)")
            return false
        }
    }

    /// Sends text into a controllable session's terminal (backchannel, #724).
    /// Returns true on 200. Gated daemon-side by toggle + consent + backend.
    func sendInput(sessionId: String, text: String) async -> Bool {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/sessions/\(sessionId)/input") else { return false }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        do {
            request.httpBody = try JSONEncoder().encode(["data": text])
            let (_, response) = try await localURLSession.data(for: request)
            return (response as? HTTPURLResponse)?.statusCode == 200
        } catch {
            print("⌨️ sendInput failed: \(error.localizedDescription)")
            return false
        }
    }

    /// Delivers an interrupt (Ctrl-C) to a controllable session (#724).
    func interruptSession(sessionId: String) async -> Bool {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/sessions/\(sessionId)/interrupt") else { return false }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        do {
            let (_, response) = try await localURLSession.data(for: request)
            return (response as? HTTPURLResponse)?.statusCode == 200
        } catch {
            print("⌨️ interrupt failed: \(error.localizedDescription)")
            return false
        }
    }

    /// Starts periodic re-hydration so group-level cost values (which arrive
    /// as part of /api/v1/sessions and are not pushed via WebSocket deltas)
    /// stay fresh. Idempotent. No-op under XCTest (issue #832) — a real
    /// daemon round-trip has no business running on a timer during unit tests.
    func startProjectCostsPolling() {
        guard !isRunningUnitTests else { return }
        projectCostsTimer?.invalidate()
        projectCostsTimer = Timer.scheduledTimer(withTimeInterval: projectCostsRefreshInterval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                await self?.hydrateSessions()
            }
        }
    }

    func hydrateSessions() async {
        // The Local source feeds sessionMap/localApiGroups from the local
        // daemon's REST API. When Local is disabled this must not run, or the
        // 30s cost-poll timer (and any pending rehydration) would silently
        // re-add the local sessions that disabling the source just cleared.
        guard useLocalDaemon else { return }
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/sessions") else { return }
        do {
            let (data, response) = try await localURLSession.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            let decoder = JSONDecoder()
            let payload = try decoder.decode(SessionsResponse.self, from: data)
            let topGroups = payload.groups
            providerCosts = payload.providerCosts ?? [:]

            localApiGroups = topGroups
            recomposeApiGroups()
            if isDebugMode {
                print("💧 hydrate: groupedSessionIds=\(groupedSessionIds.count) ids, sample=\(groupedSessionIds.prefix(3))")
            }

            // Flatten all groups → sessions (including nested sub-groups and children).
            var states: [SessionState] = []
            var hasGasTown = false
            for group in topGroups {
                if group.type == "gastown" { hasGasTown = true }
                states += flattenAgents(from: group)
            }
            sessionMap = Dictionary(uniqueKeysWithValues: states.map { ($0.id, $0) })
            rebuildSessionsFromMap()

            // Forward Gas Town availability to provider.
            hasGasTownGroups = hasGasTown
            gasTownProvider?.updateAvailability(hasGasTown)

            print("💧 Hydrated \(states.count) sessions from REST API")
        } catch {
            print("💧 Hydration failed: \(error.localizedDescription)")
        }
    }

    /// Schedule a debounced re-hydration (for structural changes like session deletion).
    func scheduleRehydration() {
        rehydrationTask?.cancel()
        rehydrationTask = Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 0.5s debounce
            guard !Task.isCancelled else { return }
            await hydrateSessions()
        }
    }
}
