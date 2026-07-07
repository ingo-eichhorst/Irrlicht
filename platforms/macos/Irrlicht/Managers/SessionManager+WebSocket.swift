import Foundation

// MARK: - Local WebSocket connection + message ingestion
//
// Split out of SessionManager.swift (#807): everything involved in owning the
// local daemon's WebSocket — connect/reconnect, decoding pushed frames, and
// coalescing bursts of `session_updated` pushes into bounded UI refreshes.
// State lives on SessionManager itself; this file only adds behavior.

extension SessionManager {
    // MARK: - WebSocket

    func startWebSocket() {
        guard connectionState == .disconnected else { return }
        connectionState = .connecting
        resetLocalConnectBackoff()
        scheduleConnect(after: 0)
    }

    /// Clears backoff/failure state for a fresh connect cycle. Split out of
    /// `startWebSocket()` so it's unit-testable without also triggering a
    /// live `scheduleConnect()` dial (#843).
    func resetLocalConnectBackoff() {
        reconnectDelay = 1.0
        consecutiveLocalConnectFailures = 0
        localConnectionStalled = false
    }

    func stopWebSocket() {
        connectTask?.cancel()
        connectTask = nil
        webSocketTask?.cancel(with: .normalClosure, reason: nil)
        webSocketTask = nil
        // Cancel a pending debounced rehydration too, so disabling the Local
        // source can't be undone ~0.5s later by an in-flight hydrateSessions().
        rehydrationTask?.cancel()
        rehydrationTask = nil
        connectionState = .disconnected
        localConnectionStalled = false
    }

    func scheduleConnect(after delay: TimeInterval) {
        connectTask = Task { [weak self] in
            guard let self else { return }
            if delay > 0 {
                try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            }
            guard !Task.isCancelled else { return }
            await self.connect()
        }
    }

    func connect() async {
        await hydrateAgents()
        await hydrateSessions()
        await refreshPermissions()

        guard let url = URL(string: "\(DaemonEndpoint.wsBase)/api/v1/sessions/stream") else { return }
        let task = localURLSession.webSocketTask(with: url)
        webSocketTask = task
        task.resume()

        lastPushSeq = 0 // fresh stream, fresh seq cursor (#600)

        // `resume()` returns before the WebSocket handshake is known good —
        // the HTTP upgrade itself can still fail — so declaring "connected"
        // (and resetting the backoff/failure streak) any earlier defeated
        // the exponential backoff entirely: every attempt reset
        // `reconnectDelay` to 1.0 before failing, so a dead daemon got
        // hammered every ~1.1s forever instead of backing off (#843).
        //
        // A daemon with no tracked sessions can go the entire cycle without
        // pushing a single application message, so waiting on `receive()`
        // alone would never confirm a perfectly healthy idle connection —
        // send a protocol-level ping right away too. Whichever signal wins
        // the race calls `recordConfirmedLocalConnect()`, which is
        // idempotent (`connectionState` gates it) so there's no double-fire.
        task.sendPing { [weak self] error in
            guard error == nil else { return }
            Task { @MainActor [weak self] in
                guard let self, self.webSocketTask === task else { return }
                self.recordConfirmedLocalConnect()
            }
        }

        do {
            while !Task.isCancelled {
                let message = try await task.receive()
                recordConfirmedLocalConnect()
                switch message {
                case .string(let text):
                    handleWsMessage(text)
                case .data(let data):
                    if let text = String(data: data, encoding: .utf8) {
                        handleWsMessage(text)
                    }
                @unknown default:
                    break
                }
            }
        } catch {
            print("🔌 WebSocket disconnected: \(error.localizedDescription)")
        }

        guard connectionState != .disconnected && !Task.isCancelled else { return }

        // Neither the ping nor hydration nor the socket ever confirmed
        // anything, even though `resume()` said the attempt started
        // fine. A stuck OS-level connection cache pinned to this
        // URLSession instance can cause exactly that — failing forever
        // against a healthy daemon that restarted on the same port —
        // until something discards it (previously only an app relaunch;
        // #843). Recycle it ourselves once failures pile up rather than
        // waiting on that.
        if connectionState != .connected && recordFailedLocalConnectAttempt() {
            print("🔌 Local daemon unreachable after repeated attempts — recreating URLSession")
        }

        let jitter = Double.random(in: 0...(reconnectDelay * 0.2))
        let delay = reconnectDelay + jitter
        connectionState = .reconnecting
        print("🔌 Reconnecting in \(String(format: "%.1f", delay))s")

        reconnectDelay = min(reconnectDelay * 2, maxReconnectDelay)
        scheduleConnect(after: delay)
    }

    /// Applied once a reconnect attempt's WebSocket is confirmed alive — a
    /// successful ping/pong or an arrived message, whichever comes first.
    /// Idempotent (gated on `connectionState`) since both signals race for
    /// the same cycle. Split out of `connect()` so the state transition is
    /// unit-testable without a live socket (#843).
    func recordConfirmedLocalConnect() {
        guard connectionState != .connected else { return }
        reconnectDelay = 1.0
        consecutiveLocalConnectFailures = 0
        localConnectionStalled = false
        connectionState = .connected
        print("🔌 WebSocket connected to irrlichd")
    }

    /// Applied once a reconnect attempt's hydration + WebSocket both come
    /// back empty. Bumps the failure streak and, once it crosses
    /// `localConnectFailuresBeforeSessionRecycle`, recycles `localURLSession`
    /// and flags the connection as stalled so the UI can show something
    /// stronger than "reconnecting" (#843). Returns whether it recycled, for
    /// logging. Split out of `connect()` for unit testability.
    @discardableResult
    func recordFailedLocalConnectAttempt() -> Bool {
        consecutiveLocalConnectFailures += 1
        guard consecutiveLocalConnectFailures >= localConnectFailuresBeforeSessionRecycle else { return false }
        localURLSession.invalidateAndCancel()
        localURLSession = URLSession(configuration: .ephemeral)
        consecutiveLocalConnectFailures = 0
        localConnectionStalled = true
        return true
    }

    // MARK: - Inbound message decoding

    struct WsEnvelope: Decodable {
        let type: String
        let session: SessionState?
        // Daemon-global monotonic push counter (#593). Optional so connect
        // snapshots and older daemons (no seq) still decode.
        let seq: UInt64?

        // History-message fields (snapshot/tick/upgrade).
        let sessionID: String?
        let history: [String: String]?
        let granularitySec: Int?
        let buckets: [String: Int8]?
        let priority: Int8?
        // Tick generations: keyed by granularity for snapshots, by session_id
        // for ticks (parallel to `buckets`). Optional so older daemons still
        // decode.
        let generations: [String: UInt64]?
        let bucketGenerations: [String: UInt64]?

        // input_requested payload (#724): the daemon asking us to inject into
        // an AppleScript-only backend (iTerm2/Terminal.app).
        let input: InputRequestPayload?

        enum CodingKeys: String, CodingKey {
            case type
            case session
            case seq
            case sessionID         = "session_id"
            case history
            case granularitySec    = "granularity_sec"
            case buckets
            case priority
            case generations
            case bucketGenerations = "bucket_generations"
            case input
        }
    }

    /// Payload of a PushTypeInputRequested message.
    struct InputRequestPayload: Decodable {
        let action: String   // "input" | "interrupt"
        let data: String?
    }

    func handleWsMessage(_ text: String) {
        guard let data = text.data(using: .utf8) else { return }
        do {
            let envelope = try JSONDecoder().decode(WsEnvelope.self, from: data)
            // A gap in the daemon-global push seq means the daemon skipped this
            // client (slow-subscriber drop) — re-hydrate now instead of waiting
            // for the next structural event (#600). seq 0/absent = unstamped
            // (connect snapshots, older daemons); a backward jump is a daemon
            // restart — adopt the cursor without rehydrating.
            if let seq = envelope.seq, seq > 0 {
                if lastPushSeq > 0 && seq > lastPushSeq + 1 {
                    scheduleRehydration()
                }
                lastPushSeq = seq
            }
            switch envelope.type {
            case "session_created":
                if let session = envelope.session {
                    sessionMap[session.id] = session
                    rebuildSessionsFromMap()
                    scheduleRehydration() // group structure may have changed
                }
            case "session_updated":
                if var session = envelope.session {
                    let oldState = sessionMap[session.id]?.state
                    // Preserve role metadata from hydration (WS sends raw sessions without it).
                    if let existing = sessionMap[session.id] {
                        session = session.preservingRole(from: existing)
                    }
                    sessionMap[session.id] = session
                    if isDebugMode {
                        let cost = session.metrics?.estimatedCostUSD ?? 0
                        let ctx = session.metrics?.contextUtilization ?? 0
                        let inGroups = groupedSessionIds.contains(session.id)
                        print("📨 session_updated id=\(session.id) state=\(session.state.rawValue) cost=\(cost) ctx=\(ctx) inGroupedIds=\(inGroups)")
                    }
                    let stateChanged = oldState != nil && oldState != session.state
                    if let old = oldState, old != session.state {
                        checkStateTransitionNotification(session: session, previousState: old)
                    }
                    // #690: coalesce the expensive rebuild + apiGroups patch. With
                    // 40+ working agents each ticking metrics, this case used to
                    // re-render the whole (eager, non-virtualized) list once per
                    // push. Batch pure-metric updates into one refresh per window;
                    // flush state transitions immediately so they stay snappy
                    // (transitions fire at human pace, not metric-tick rate).
                    enqueueUIRefresh(for: session.id, immediate: stateChanged)
                }
            case "session_deleted":
                if let session = envelope.session {
                    purgeSessionState(sessionId: session.id)
                    scheduleRehydration()
                }
            case "focus_requested":
                // A client (irrlicht-focus CLI or third-party integration) has
                // asked the daemon to bring this session's host window forward.
                // The daemon always includes the full session in this envelope.
                // Must dispatch to main — SessionLauncher is @MainActor-adjacent.
                if let session = envelope.session {
                    DispatchQueue.main.async {
                        SessionLauncher.jump(session)
                    }
                }
            case "input_requested":
                // The daemon can't script iTerm2/Terminal.app directly (no
                // Automation TCC grant); it delegates the injection to us (#724).
                if let session = envelope.session, let input = envelope.input {
                    DispatchQueue.main.async {
                        SessionInputActivator.inject(session, action: input.action, data: input.data ?? "")
                    }
                }
            case "history_snapshot":
                if let sid = envelope.sessionID, let hist = envelope.history {
                    applyHistorySnapshot(sessionID: sid, history: hist, generations: envelope.generations)
                }
            case "history_tick":
                if let gran = envelope.granularitySec, let bks = envelope.buckets {
                    applyHistoryTick(granularitySec: gran, buckets: bks, bucketGenerations: envelope.bucketGenerations)
                }
            case "history_upgrade":
                if let sid = envelope.sessionID, let prio = envelope.priority {
                    applyHistoryUpgrade(sessionID: sid, priority: prio)
                }
            case "permissions_updated":
                // Consent state changed (agent detected, or the web
                // dashboard answered the wizard). Dataless by design —
                // re-fetch; pendingWizardAgents drives the overlay (#570).
                Task { await refreshPermissions() }
            default:
                break
            }
        } catch {
            print("⚠️ Failed to decode WS message: \(error.localizedDescription)")
        }
    }

    // MARK: - Coalesced UI refresh (#690)
    //
    // With 40+ working agents each emitting frequent metric ticks, every
    // `session_updated` used to run a full `rebuildSessionsFromMap()` +
    // `patchApiGroups()` and reassign @Published surfaces. Because each WS
    // message resumes as its own @MainActor turn, SwiftUI re-rendered the whole
    // (eager, non-virtualized) session list once per message — O(N) work × N
    // pushes/sec. We coalesce pure-metric updates: accumulate dirty session ids
    // and apply one rebuild + one batched patch per refresh window, so render
    // volume is bounded by the window, not the push rate. State transitions and
    // the notifications that depend on them (ready/waiting sounds and banners)
    // still fire synchronously at message time; only context-pressure alerts —
    // which ride `rebuildSessionsFromMap` — are deferred, by at most one window.

    /// Queue a session for the next coalesced UI refresh. `immediate` bypasses
    /// the window (used for state transitions, which are rare and want to feel
    /// instant); the metric-tick storm always coalesces.
    func enqueueUIRefresh(for sessionID: String, immediate: Bool) {
        pendingDirtySessionIDs.insert(sessionID)
        if immediate {
            flushUIRefresh()
            return
        }
        guard !uiRefreshScheduled else { return }
        uiRefreshScheduled = true
        Timer.scheduledTimer(withTimeInterval: uiRefreshInterval, repeats: false) { [weak self] _ in
            Task { @MainActor [weak self] in self?.flushUIRefresh() }
        }
    }

    /// Apply all pending session updates in one pass: a single flat-surface
    /// rebuild plus one batched apiGroups patch. Both surfaces reassign in this
    /// one synchronous turn, so SwiftUI coalesces them into a single render.
    func flushUIRefresh() {
        uiRefreshScheduled = false
        guard !pendingDirtySessionIDs.isEmpty else { return }
        let dirty = pendingDirtySessionIDs
        pendingDirtySessionIDs.removeAll()
        uiRefreshFlushCount += 1
        rebuildSessionsFromMap()
        patchApiGroups(sessions: dirty.compactMap { sessionMap[$0] })
    }

    /// Batch counterpart to `patchApiGroups(session:)` for the coalesced flush:
    /// applies every dirty session to the group tree in a single map pass and
    /// recomposes once, rather than one full map + recompose per session (#690).
    /// A session with no row yet (created mid-window) self-heals via the same
    /// debounced rehydration the single-session path uses.
    func patchApiGroups(sessions: [SessionState]) {
        let present = sessions.filter { groupedSessionIds.contains($0.id) }
        if !present.isEmpty {
            localApiGroups = localApiGroups.map { group in
                present.reduce(group) { patchGroup($0, with: $1) }
            }
            recomposeApiGroups()
        }
        if sessions.contains(where: { !groupedSessionIds.contains($0.id) }) {
            scheduleRehydration()
        }
    }

    /// Test seam: synchronously apply any pending coalesced refresh.
    func flushPendingUIRefreshForTests() {
        flushUIRefresh()
    }

    func rebuildSessionsFromMap() {
        // Merge relay-only sessions (ids not present locally) so the cycle and
        // menu-bar counts include other machines' sessions. Local wins on id
        // collision — the same daemon reached via both sources shows once.
        let localIDs = Set(sessionMap.keys)
        // Local-wins name-collapse (#746, #828): a project the local daemon
        // publishes and also subscribes back to via the relay echoes with a
        // drifted session_id that escapes the id-only filter below. Mirrors
        // recomposeApiGroups()/relayGroups()'s collapse so the flat `sessions`
        // array (menu bar, cycling) agrees with `apiGroups` (list view) instead
        // of double-counting the echo.
        let localProjectNames = Set(localApiGroups.map(\.name))
        // Two different relay daemons sharing a session_id stay distinct (keyed
        // by compound rowID), so build a flat array rather than a bare-id dict
        // — a dict would collide them back into one row (#537).
        let relayOnly = relaySessionMap.values.filter {
            !localIDs.contains($0.id) && !localProjectNames.contains($0.projectName ?? "")
        }
        let all = Array(sessionMap.values) + relayOnly

        // Exclude child sessions (subagents) from the main session list so they
        // don't appear in the cycle or as separate rows. A parent must resolve
        // within the same scope: a relay child against its own daemon's
        // sessions (rowIDs are "daemon/id"), a local child against the local set.
        let relayParentKeys = Set(relaySessionMap.keys)
        var topLevel = all.filter { session in
            guard let pid = session.parentSessionId else { return true }
            if let dID = session.daemonID {
                return !relayParentKeys.contains("\(dID)/\(pid)")
            }
            return !localIDs.contains(pid)
        }
        topLevel = sortSessionsByOrder(topLevel)
        updateSessionOrder(with: topLevel)
        assignDuplicateIndexes(&topLevel)
        sessions = topLevel
        allSessions = all // includes children for badge counting
        checkContextPressureAlerts(sessions: topLevel)
        writeDebugState()
    }
}
