import Foundation

// MARK: - Sources reconciliation + relay WebSocket (multi-source)
//
// Split out of SessionManager.swift (#807). A second, optional connection to
// a standalone irrlichtrelay. It speaks the relay envelope (a `hello`
// handshake, then `push`-wrapped frames); the local connection speaks raw
// daemon frames. Relay sessions are held in their own map so the 30s local
// re-hydration — which replaces `sessionMap` wholesale — can never drop them.
// A relay session whose id also exists locally collapses to the local copy on
// merge, so the same daemon reached over both paths shows once.

extension SessionManager {
    var useLocalDaemon: Bool { UserDefaults.standard.bool(forKey: "useLocalDaemon") }
    var useRelayServer: Bool { UserDefaults.standard.bool(forKey: "useRelayServer") }
    var relayServerURL: String {
        (UserDefaults.standard.string(forKey: "relayServerURL") ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    // MARK: - Sources reconciliation

    /// Diffs the current Sources config against the last applied one and
    /// reconnects only on change. Cheap to call on every UserDefaults change.
    /// Under XCTest this is a no-op (issue #832) — every SessionManager()
    /// built by a test would otherwise dial the real local daemon (both here
    /// and via the `UserDefaults.didChangeNotification` observer any test's
    /// `defaults.set(...)` fires), racing snapshot/state assertions against
    /// whatever daemon happens to be reachable on the machine.
    func sourcesSettingsChanged() {
        guard !isRunningUnitTests else { return }
        let cfg = "\(useLocalDaemon)|\(useRelayServer)|\(relayServerURL)"
        guard cfg != lastSourceConfig else { return }
        lastSourceConfig = cfg
        reconcileSources()
    }

    /// Brings the live connections in line with the Sources settings: starts
    /// or stops the local and relay links, restarting the relay if its URL
    /// changed.
    func reconcileSources() {
        if useLocalDaemon {
            if connectionState == .disconnected { startWebSocket() }
        } else if connectionState != .disconnected {
            stopWebSocket()
            sessionMap.removeAll()
            localApiGroups.removeAll()
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }

        let url = relayServerURL
        if useRelayServer && !url.isEmpty {
            if relayConnectionState == .disconnected || url != activeRelayURL {
                stopRelay()
                activeRelayURL = url
                startRelay()
            }
        } else if relayConnectionState != .disconnected {
            stopRelay()
        }
    }

    /// Called when the relay bearer token changes in Settings. The token lives
    /// in the Keychain, not UserDefaults, so it never fires the didChangeNotification
    /// that drives sourcesSettingsChanged — and after a 4401 the relay is parked
    /// in .disconnected with reconnect disabled. Force a fresh connect with the
    /// new credential so correcting the token recovers without an app restart.
    func relayTokenDidChange() {
        guard useRelayServer, !relayServerURL.isEmpty else { return }
        stopRelay()
        activeRelayURL = relayServerURL
        startRelay()
    }

    // MARK: - Relay WebSocket

    func startRelay() {
        guard relayConnectionState == .disconnected else { return }
        relayConnectionState = .connecting
        resetRelayConnectBackoff()
        scheduleRelayConnect(after: 0)
    }

    /// Clears backoff/failure state for a fresh relay connect cycle. Mirrors
    /// `resetLocalConnectBackoff()` (#846).
    func resetRelayConnectBackoff() {
        relayReconnectDelay = 1.0
        consecutiveRelayConnectFailures = 0
        relayConnectionStalled = false
    }

    func stopRelay() {
        relayConnectTask?.cancel()
        relayConnectTask = nil
        relayWebSocketTask?.cancel(with: .normalClosure, reason: nil)
        relayWebSocketTask = nil
        relayConnectionState = .disconnected
        relayConnectionStalled = false
        relayDaemons.removeAll()
        if !relaySessionMap.isEmpty {
            relaySessionMap.removeAll()
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }
    }

    func scheduleRelayConnect(after delay: TimeInterval) {
        relayConnectTask = Task { [weak self] in
            guard let self else { return }
            if delay > 0 {
                try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            }
            guard !Task.isCancelled else { return }
            await self.relayConnect()
        }
    }

    func relayConnect() async {
        guard let url = relayStreamURL(activeRelayURL) else {
            relayConnectionState = .disconnected
            return
        }
        let task = relayURLSession.webSocketTask(with: url)
        relayWebSocketTask = task
        task.resume()
        relayConnectionState = .connected
        print("🔌 Relay connected to \(activeRelayURL)")

        // Announce as a client so the relay streams enveloped frames. Harmless
        // if the URL actually points at a daemon (it ignores the frame).
        var hello: [String: Any] = ["type": "hello", "protocol_version": 1, "role": "client"]
        let relayToken = KeychainStore.get(account: "relayToken")
        if !relayToken.isEmpty { hello["token"] = relayToken }
        if let helloData = try? JSONSerialization.data(withJSONObject: hello),
           let helloStr = String(data: helloData, encoding: .utf8) {
            try? await task.send(.string(helloStr))
        }

        var confirmed = false
        do {
            while !Task.isCancelled {
                let message = try await task.receive()
                // Reset the backoff only once the relay actually delivers a
                // frame. resume() returns before the connection is known good,
                // so resetting there would defeat the backoff and spin a ~1/s
                // reconnect loop against an unreachable relay.
                if !confirmed {
                    confirmed = true
                    recordConfirmedRelayConnect()
                }
                switch message {
                case .string(let text):
                    handleRelayMessage(text)
                case .data(let data):
                    if let text = String(data: data, encoding: .utf8) { handleRelayMessage(text) }
                @unknown default:
                    break
                }
            }
        } catch {
            print("🔌 Relay disconnected: \(error.localizedDescription)")
        }

        // The cycle closed without ever confirming a frame — the same
        // stuck-URLSession failure mode #843 fixed for the local daemon can
        // strand the relay link the same way if `irrlichtrelay` restarts on
        // the same host:port while a client is connected. Recycle
        // `relayURLSession` once failures pile up rather than waiting on an
        // app relaunch (#846). A 4401 (rejected token, handled below) isn't a
        // wedged connection, so it doesn't count toward the streak.
        if !confirmed && task.closeCode.rawValue != 4401 {
            if recordFailedRelayConnectAttempt() {
                print("🔌 Relay unreachable after repeated attempts — recreating URLSession")
            }
        }

        // The link is down, so we no longer know the remote state: drop the
        // relay's daemons and sessions in BOTH the reconnect and the auth-failed
        // path — otherwise a remote session that ended during the outage lingers
        // as a ghost row (the relay's replay can only re-add survivors, never
        // signal the deletions we missed). The relay's replay rebuilds the live
        // set on reconnect; sessions also present locally stay (they live in
        // sessionMap, which local wins).
        relayDaemons.removeAll()
        if !relaySessionMap.isEmpty {
            relaySessionMap.removeAll()
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }

        // A 4401 close means the relay rejected or revoked our bearer token.
        // Retrying with the same credential just loops, so stop reconnecting and
        // wait for the user to fix the token (relayTokenDidChange restarts us).
        if task.closeCode.rawValue == 4401 {
            relayConnectionState = .disconnected
            print("🔌 Relay auth failed (4401) — check the relay token in Settings; not reconnecting")
            return
        }
        guard relayConnectionState != .disconnected && !Task.isCancelled else { return }
        let jitter = Double.random(in: 0...(relayReconnectDelay * 0.2))
        let delay = relayReconnectDelay + jitter
        relayConnectionState = .reconnecting
        relayReconnectDelay = min(relayReconnectDelay * 2, maxReconnectDelay)
        scheduleRelayConnect(after: delay)
    }

    /// Applied once a relay reconnect attempt's WebSocket is confirmed alive
    /// by an arrived frame. Mirrors `recordConfirmedLocalConnect()` for the
    /// relay path (#846).
    func recordConfirmedRelayConnect() {
        relayReconnectDelay = 1.0
        consecutiveRelayConnectFailures = 0
        relayConnectionStalled = false
    }

    /// Applied once a relay reconnect attempt's cycle closes without ever
    /// confirming a frame. Bumps the failure streak and, once it crosses
    /// `relayConnectFailuresBeforeSessionRecycle`, recycles `relayURLSession`
    /// and flags the connection as stalled. Returns whether it recycled, for
    /// logging. Mirrors `recordFailedLocalConnectAttempt()` (#843) for the
    /// relay path (#846).
    @discardableResult
    func recordFailedRelayConnectAttempt() -> Bool {
        consecutiveRelayConnectFailures += 1
        guard consecutiveRelayConnectFailures >= relayConnectFailuresBeforeSessionRecycle else { return false }
        relayURLSession.invalidateAndCancel()
        relayURLSession = URLSession(configuration: .ephemeral)
        consecutiveRelayConnectFailures = 0
        relayConnectionStalled = true
        return true
    }

    /// Normalizes a user-entered relay address into a ws(s):// stream URL.
    /// Accepts http(s)://, ws(s)://, or a bare host[:port], with or without
    /// the stream path. Mirrors the web dashboard's relayWsUrl().
    func relayStreamURL(_ raw: String) -> URL? {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return nil }
        if s.hasPrefix("http://") { s = "ws://" + s.dropFirst("http://".count) }
        else if s.hasPrefix("https://") { s = "wss://" + s.dropFirst("https://".count) }
        if !s.hasPrefix("ws://") && !s.hasPrefix("wss://") { s = "ws://" + s }
        while s.hasSuffix("/") { s.removeLast() }
        if !s.hasSuffix("/api/v1/sessions/stream") { s += "/api/v1/sessions/stream" }
        return URL(string: s)
    }

    struct RelayFrameType: Decodable { let type: String }

    struct RelayPush: Decodable {
        let type: String
        let source: String?
        let msg: WsEnvelope?
    }

    struct RelayDaemonInfo: Decodable {
        let daemonID: String
        let daemonLabel: String?
        let status: String?
        enum CodingKeys: String, CodingKey {
            case daemonID = "daemon_id"
            case daemonLabel = "daemon_label"
            case status
        }
    }

    struct RelayControl: Decodable {
        let type: String
        let daemons: [RelayDaemonInfo]?
        let daemonID: String?
        let daemonLabel: String?
        let status: String?
        enum CodingKeys: String, CodingKey {
            case type, daemons, status
            case daemonID = "daemon_id"
            case daemonLabel = "daemon_label"
        }
    }

    func handleRelayMessage(_ text: String) {
        guard let data = text.data(using: .utf8),
              let kind = (try? JSONDecoder().decode(RelayFrameType.self, from: data))?.type else { return }
        switch kind {
        case "push":
            if let push = try? JSONDecoder().decode(RelayPush.self, from: data), let inner = push.msg {
                applyRelayInner(inner, daemonID: push.source)
            }
        case "snapshot":
            if let ctrl = try? JSONDecoder().decode(RelayControl.self, from: data) {
                relayDaemons.removeAll()
                for d in ctrl.daemons ?? [] {
                    relayDaemons[d.daemonID] = d.daemonLabel ?? d.daemonID
                    // A daemon we were fading is back — drop its stale rows and
                    // the offline mark; its fresh state arrives as pushes.
                    if offlineDaemons[d.daemonID] != nil { restoreDaemon(d.daemonID) }
                }
            }
        case "daemon_status":
            if let ctrl = try? JSONDecoder().decode(RelayControl.self, from: data), let id = ctrl.daemonID {
                if ctrl.status == "disconnected" {
                    // Fade, don't delete (#540): keep the daemon's rows on screen
                    // (the relay no longer deletes them) and remember its label so
                    // the faded rows keep their tooltip. The view dims them via
                    // `isOffline`.
                    let label = relayDaemons.removeValue(forKey: id) ?? offlineDaemons[id] ?? id
                    offlineDaemons[id] = label
                } else {
                    relayDaemons[id] = ctrl.daemonLabel ?? id
                    if offlineDaemons[id] != nil { restoreDaemon(id) }
                }
            }
        case "hello_ack":
            break
        default:
            // The URL pointed at a daemon (raw frames): handle it like one.
            // A raw daemon frame has no envelope source, so it has no daemon id.
            if let inner = try? JSONDecoder().decode(WsEnvelope.self, from: data) {
                applyRelayInner(inner, daemonID: nil)
            }
        }
    }

    /// Applies a relay session frame into the relay-only map. History and
    /// focus frames are ignored for relay sources in v0 (history bars for
    /// relay-only sessions are deferred; focus is host-local).
    ///
    /// The relay Push envelope's `source` (daemon id) is stamped onto the
    /// session and folded into its `rowID`, so two daemons sharing a session_id
    /// stay distinct in `relaySessionMap` instead of colliding (#537). The
    /// local-collapse dedup still compares bare `id` against `sessionMap`.
    func applyRelayInner(_ env: WsEnvelope, daemonID: String?) {
        switch env.type {
        case "session_created", "session_updated":
            if var s = env.session {
                s.daemonID = daemonID
                // Notify on a state transition for a relay-only session. One
                // also present locally is handled by the local path, so
                // notifying here too would double-fire.
                if sessionMap[s.id] == nil, let old = relaySessionMap[s.rowID]?.state, old != s.state {
                    checkStateTransitionNotification(session: s, previousState: old)
                }
                relaySessionMap[s.rowID] = s
                rebuildSessionsFromMap()
                recomposeApiGroups()
            }
        case "session_deleted":
            if var s = env.session {
                s.daemonID = daemonID
                if relaySessionMap.removeValue(forKey: s.rowID) != nil {
                    rebuildSessionsFromMap()
                    recomposeApiGroups()
                }
            }
        default:
            break
        }
    }

    /// A relay session is faded when its daemon is currently offline (#540).
    func isOffline(_ session: SessionState) -> Bool {
        guard let id = session.daemonID else { return false }
        return offlineDaemons[id] != nil
    }

    /// A faded daemon reconnected: drop its kept rows (any that ended while it
    /// was offline are gone for good; live ones re-arrive as fresh pushes) and
    /// clear the offline mark so its rows render solid again.
    func restoreDaemon(_ id: String) {
        offlineDaemons.removeValue(forKey: id)
        let before = relaySessionMap.count
        relaySessionMap = relaySessionMap.filter { $0.value.daemonID != id }
        if relaySessionMap.count != before {
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }
    }

    // MARK: - Aggregate connection status (local + relay)

    /// One line per source for the connection-status tooltip: the local daemon
    /// (when enabled) plus each daemon the relay reports. Falls back to the
    /// single-source tooltip when nothing is configured.
    var connectionTooltip: String {
        var lines: [String] = []
        if useLocalDaemon {
            let stalledSuffix = localConnectionStalled ? " (daemon unreachable — retrying)" : ""
            lines.append("Local — \(connectionState.shortLabel)\(stalledSuffix)")
        }
        if useRelayServer && !relayServerURL.isEmpty {
            if relayConnectionState == .connected && !relayDaemons.isEmpty {
                for label in relayDaemons.values.sorted() {
                    lines.append("\(label) — connected")
                }
            } else {
                let stalledSuffix = relayConnectionStalled ? " (relay unreachable — retrying)" : ""
                lines.append("\(relayServerURL) — \(relayConnectionState.shortLabel)\(stalledSuffix)")
            }
        }
        return lines.isEmpty ? connectionState.tooltip : lines.joined(separator: "\n")
    }

    /// Aggregate connection state across enabled sources for the header dot:
    /// connected wins, then connecting, then reconnecting, else disconnected.
    var aggregateConnectionState: ConnectionState {
        let states = [
            useLocalDaemon ? connectionState : nil,
            (useRelayServer && !relayServerURL.isEmpty) ? relayConnectionState : nil,
        ].compactMap { $0 }
        if states.isEmpty { return .disconnected }
        if states.contains(.connected) { return .connected }
        if states.contains(.connecting) { return .connecting }
        if states.contains(.reconnecting) { return .reconnecting }
        return .disconnected
    }
}
