import Foundation
import AppKit
import Combine
import Darwin
import SwiftUI
@preconcurrency import UserNotifications

enum ConnectionState {
    case disconnected   // not started or explicitly stopped
    case connecting     // initial connection attempt
    case connected      // WebSocket active and receiving
    case reconnecting   // transient disconnect, auto-recovering

    var tooltip: String {
        switch self {
        case .connected:    return "Daemon connected — watching for sessions"
        case .connecting:   return "Connecting to daemon\u{2026}"
        case .reconnecting: return "Reconnecting to daemon\u{2026}"
        case .disconnected: return "Daemon disconnected"
        }
    }

    /// Compact one-word label for the per-source line in the multi-source
    /// connection tooltip (e.g. "Local — connected").
    var shortLabel: String {
        switch self {
        case .connected:    return "connected"
        case .connecting:   return "connecting"
        case .reconnecting: return "reconnecting"
        case .disconnected: return "disconnected"
        }
    }

    var dotColor: Color {
        switch self {
        case .connected:    return .green
        case .connecting:   return .yellow
        case .reconnecting: return .yellow
        case .disconnected: return Color(.tertiaryLabelColor)
        }
    }
}

@MainActor
class SessionManager: ObservableObject {
    @Published var sessions: [SessionState] = []
    @Published var allSessions: [SessionState] = [] // includes child sessions for badge counting
    @Published var connectionState: ConnectionState = .disconnected
    @Published var lastError: String?
    @Published var apiGroups: [AgentGroup] = []  // recursive group structure from API
    @Published var providerCosts: [String: [String: Double]] = [:]  // providerKey → timeframe → USD (windowed)
    @Published var stateHistory: [String: [String]] = [:]  // session_id → oldest→newest state strings (active granularity)
    @Published var historyBucketCount: Int = 60          // matches HistoryBucketCount on the daemon

    /// All three granularities held in parallel so toggling 1s/10s/60s is
    /// instant — the WebSocket streams every granularity continuously, the
    /// view just picks which dict to mirror into `stateHistory`.
    private var historyByGranularity: [Int: [String: [String]]] = [1: [:], 10: [:], 60: [:]]
    /// Per-session per-granularity high-water-mark of applied tick generations.
    /// Lets us drop a tick that's already reflected in our snapshot — closing
    /// the connect-time race where the daemon emits a tick between snapshot
    /// generation and the WebSocket flushing the snapshot to us.
    private var lastTickGen: [String: [Int: UInt64]] = [:]
    private var currentHistoryGranularitySec: Int = 1

    /// Group names the user has collapsed. Lifted out of GroupView's local
    /// state so (a) SessionListView's size estimator can skip collapsed
    /// groups, and (b) collapse state survives apiGroups re-assignments.
    @Published var collapsedGroupNames: Set<String> = []

    /// Timer that periodically re-hydrates sessions so group-level cost
    /// values (which only ride the /api/v1/sessions response) stay fresh —
    /// WebSocket deltas only carry individual session updates.
    private var projectCostsTimer: Timer?
    private let projectCostsRefreshInterval: TimeInterval = 30.0

    /// Daemon-owned directory. `irrlichd` creates it and writes session JSON
    /// files; the app only mutates individual files for explicit user actions
    /// (`resetSessionState`, `deleteSession`). Reads go through the WebSocket.
    private let instancesPath: URL
    private let orderFilePath: URL
    private var sessionOrder: [String] = []

    // Project group ordering (persisted in UserDefaults)
    @Published var projectGroupOrder: [String] = []
    private let projectGroupOrderKey = "projectGroupOrder"

    // Tracks which pressure thresholds (80, 95) have already fired a notification
    // for each session ID. Prevents re-firing on every state update.
    private var notifiedThresholds: [String: Set<Int>] = [:]

    // MARK: - WebSocket state

    private var webSocketTask: URLSessionWebSocketTask?
    private var connectTask: Task<Void, Never>?
    private var reconnectDelay: TimeInterval = 1.0
    private let maxReconnectDelay: TimeInterval = 30.0
    var sessionMap: [String: SessionState] = [:]

    // MARK: - Relay source (multi-source)
    // A second, optional connection to a standalone irrlichtrelay. It speaks
    // the relay envelope (a `hello` handshake, then `push`-wrapped frames);
    // the local connection above speaks raw daemon frames. Relay sessions are
    // held in their own map so the 30s local re-hydration — which replaces
    // `sessionMap` wholesale — can never drop them. A relay session whose id
    // also exists locally collapses to the local copy on merge, so the same
    // daemon reached over both paths shows once.

    private var relayWebSocketTask: URLSessionWebSocketTask?
    private var relayConnectTask: Task<Void, Never>?
    private var relayReconnectDelay: TimeInterval = 1.0
    @Published var relayConnectionState: ConnectionState = .disconnected
    /// Relay-sourced sessions, keyed by session id.
    private var relaySessionMap: [String: SessionState] = [:]
    /// Daemons the relay reports connected: daemon_id → label, for the tooltip.
    /// Drives `connectionTooltip` and the per-row origin glyph tooltip (#538) —
    /// both read by the view. Not @Published, so nudge SwiftUI on every change —
    /// otherwise a daemon_status update with no accompanying session change
    /// leaves the tooltip stale.
    var relayDaemons: [String: String] = [:] {
        willSet { objectWillChange.send() }
    }
    /// Daemons that disconnected but whose rows we keep on screen, faded, until
    /// they reconnect (#540 "fade, don't delete"): daemon_id → label, so the
    /// faded rows keep their hostname tooltip even though the daemon left
    /// `relayDaemons`. A session is faded when its `daemonID` is a key here.
    var offlineDaemons: [String: String] = [:] {
        willSet { objectWillChange.send() }
    }
    /// The relay URL currently connected, so a URL change forces a reconnect.
    private var activeRelayURL: String = ""
    /// Local groups before relay groups are appended. `apiGroups` (published)
    /// is always `orderedGroups(localApiGroups) + relayGroups()`.
    private var localApiGroups: [AgentGroup] = []
    /// Last-applied source configuration, so the UserDefaults observer only
    /// reconnects when a Sources setting actually changed.
    private var lastSourceConfig: String = ""

    private var useLocalDaemon: Bool { UserDefaults.standard.bool(forKey: "useLocalDaemon") }
    private var useRelayServer: Bool { UserDefaults.standard.bool(forKey: "useRelayServer") }
    private var relayServerURL: String {
        (UserDefaults.standard.string(forKey: "relayServerURL") ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// GasTownProvider reference for forwarding Gas Town availability.
    weak var gasTownProvider: GasTownProvider? {
        didSet {
            // Re-notify availability in case hydration already ran.
            gasTownProvider?.updateAvailability(hasGasTownGroups)
        }
    }
    /// Tracks whether any group has type == "gastown" from the last hydration.
    private var hasGasTownGroups = false

    /// Forwards notification-tap events back to the manager so clicked
    /// notifications can bring the originating terminal/IDE to the front.
    /// Held strongly so UNUserNotificationCenter's weak delegate reference
    /// stays alive.
    private var notificationForwarder: NotificationClickForwarder?

    /// Source of truth for "is macOS Focus / DND active right now". Consulted
    /// when emitting notifications so we suppress sound + TTS alongside the
    /// system-suppressed banner. Injectable for tests.
    private let focusMonitor: FocusStateProviding

    init(focusMonitor: FocusStateProviding = FocusMonitor()) {
        self.focusMonitor = focusMonitor

        let homeURL = FileManager.default.homeDirectoryForCurrentUser
        let supportPath = homeURL
            .appendingPathComponent("Library")
            .appendingPathComponent("Application Support")
            .appendingPathComponent("Irrlicht")

        self.instancesPath = supportPath.appendingPathComponent("instances")
        self.orderFilePath = supportPath.appendingPathComponent("session-order.json")

        // Out-of-the-box defaults. Notifications: all three events enabled,
        // each with a distinguishable sound (Ready=Funk, Waiting=Ping,
        // Context=Sosumi). Login item: opt the user in on first launch, with
        // the gate flag tracking that we've applied the default once.
        // register(defaults:) only seeds unset keys, so it never overrides a
        // user who has explicitly picked something else.
        var defaultsSeed: [String: Any] = [
            "launchAtLogin": true,
            "didApplyDefaultLoginItem": false,
            // Sources: local on by default, relay opt-in by URL (mirrors the
            // web dashboard's enableLocalSource / enableRelaySource / relayUrl).
            "useLocalDaemon": true,
            "useRelayServer": false,
            "relayServerURL": "",
        ]
        for event in NotificationEvent.allCases {
            defaultsSeed[event.enabledKey] = true
            defaultsSeed[event.soundKey] = event.defaultSound.rawValue
        }
        UserDefaults.standard.register(defaults: defaultsSeed)

        // Reconnect sources live when a Sources setting changes (no relaunch).
        // didChangeNotification fires for any default; sourcesSettingsChanged
        // diffs and only reconnects on an actual source-config change.
        NotificationCenter.default.addObserver(
            forName: UserDefaults.didChangeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.sourcesSettingsChanged() }
        }

        Task {
            loadSessionOrder()
            loadProjectGroupOrder()
            requestNotificationPermission()
            self.sourcesSettingsChanged()
            self.startProjectCostsPolling()
        }
    }

    deinit {
        connectTask?.cancel()
        connectTask = nil
        webSocketTask?.cancel(with: .normalClosure, reason: nil)
        webSocketTask = nil
        relayConnectTask?.cancel()
        relayConnectTask = nil
        relayWebSocketTask?.cancel(with: .normalClosure, reason: nil)
        relayWebSocketTask = nil
        projectCostsTimer?.invalidate()
        projectCostsTimer = nil
    }

    // MARK: - WebSocket

    func startWebSocket() {
        guard connectionState == .disconnected else { return }
        connectionState = .connecting
        reconnectDelay = 1.0
        scheduleConnect(after: 0)
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
    }

    private func scheduleConnect(after delay: TimeInterval) {
        connectTask = Task { [weak self] in
            guard let self else { return }
            if delay > 0 {
                try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            }
            guard !Task.isCancelled else { return }
            await self.connect()
        }
    }

    private func connect() async {
        await hydrateAgents()
        await hydrateSessions()

        guard let url = URL(string: "\(DaemonEndpoint.wsBase)/api/v1/sessions/stream") else { return }
        let task = URLSession.shared.webSocketTask(with: url)
        webSocketTask = task
        task.resume()

        reconnectDelay = 1.0
        connectionState = .connected
        print("🔌 WebSocket connected to irrlichd")

        do {
            while !Task.isCancelled {
                let message = try await task.receive()
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

        let jitter = Double.random(in: 0...(reconnectDelay * 0.2))
        let delay = reconnectDelay + jitter
        connectionState = .reconnecting
        print("🔌 Reconnecting in \(String(format: "%.1f", delay))s")

        reconnectDelay = min(reconnectDelay * 2, maxReconnectDelay)
        scheduleConnect(after: delay)
    }

    // MARK: - Sources reconciliation

    /// Diffs the current Sources config against the last applied one and
    /// reconnects only on change. Cheap to call on every UserDefaults change.
    private func sourcesSettingsChanged() {
        let cfg = "\(useLocalDaemon)|\(useRelayServer)|\(relayServerURL)"
        guard cfg != lastSourceConfig else { return }
        lastSourceConfig = cfg
        reconcileSources()
    }

    /// Brings the live connections in line with the Sources settings: starts
    /// or stops the local and relay links, restarting the relay if its URL
    /// changed.
    private func reconcileSources() {
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
        relayReconnectDelay = 1.0
        scheduleRelayConnect(after: 0)
    }

    func stopRelay() {
        relayConnectTask?.cancel()
        relayConnectTask = nil
        relayWebSocketTask?.cancel(with: .normalClosure, reason: nil)
        relayWebSocketTask = nil
        relayConnectionState = .disconnected
        relayDaemons.removeAll()
        if !relaySessionMap.isEmpty {
            relaySessionMap.removeAll()
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }
    }

    private func scheduleRelayConnect(after delay: TimeInterval) {
        relayConnectTask = Task { [weak self] in
            guard let self else { return }
            if delay > 0 {
                try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            }
            guard !Task.isCancelled else { return }
            await self.relayConnect()
        }
    }

    private func relayConnect() async {
        guard let url = relayStreamURL(activeRelayURL) else {
            relayConnectionState = .disconnected
            return
        }
        let task = URLSession.shared.webSocketTask(with: url)
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

        do {
            var confirmed = false
            while !Task.isCancelled {
                let message = try await task.receive()
                // Reset the backoff only once the relay actually delivers a
                // frame. resume() returns before the connection is known good,
                // so resetting there would defeat the backoff and spin a ~1/s
                // reconnect loop against an unreachable relay.
                if !confirmed {
                    confirmed = true
                    relayReconnectDelay = 1.0
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

    /// Normalizes a user-entered relay address into a ws(s):// stream URL.
    /// Accepts http(s)://, ws(s)://, or a bare host[:port], with or without
    /// the stream path. Mirrors the web dashboard's relayWsUrl().
    private func relayStreamURL(_ raw: String) -> URL? {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return nil }
        if s.hasPrefix("http://") { s = "ws://" + s.dropFirst("http://".count) }
        else if s.hasPrefix("https://") { s = "wss://" + s.dropFirst("https://".count) }
        if !s.hasPrefix("ws://") && !s.hasPrefix("wss://") { s = "ws://" + s }
        while s.hasSuffix("/") { s.removeLast() }
        if !s.hasSuffix("/api/v1/sessions/stream") { s += "/api/v1/sessions/stream" }
        return URL(string: s)
    }

    private struct RelayFrameType: Decodable { let type: String }

    private struct RelayPush: Decodable {
        let type: String
        let source: String?
        let msg: WsEnvelope?
    }

    private struct RelayDaemonInfo: Decodable {
        let daemonID: String
        let daemonLabel: String?
        let status: String?
        enum CodingKeys: String, CodingKey {
            case daemonID = "daemon_id"
            case daemonLabel = "daemon_label"
            case status
        }
    }

    private struct RelayControl: Decodable {
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
    private func applyRelayInner(_ env: WsEnvelope, daemonID: String?) {
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
    private func restoreDaemon(_ id: String) {
        offlineDaemons.removeValue(forKey: id)
        let before = relaySessionMap.count
        relaySessionMap = relaySessionMap.filter { $0.value.daemonID != id }
        if relaySessionMap.count != before {
            rebuildSessionsFromMap()
            recomposeApiGroups()
        }
    }

    // MARK: - apiGroups composition (local + relay)

    /// Rebuilds the published `apiGroups` from the local groups plus
    /// client-side groups for relay-only sessions, and refreshes
    /// `groupedSessionIds` (used by the local patch guard) from the local set.
    private func recomposeApiGroups() {
        apiGroups = orderedGroups(localApiGroups) + relayGroups()
        groupedSessionIds = Set(localApiGroups.flatMap { collectSessionIds(from: $0) })
    }

    /// Groups relay-only sessions (ids not present locally) by project name.
    /// No orchestrator handling or child nesting in v0; the common same-daemon
    /// case yields no relay-only rows, so this only renders a genuine second
    /// daemon's sessions.
    private func relayGroups() -> [AgentGroup] {
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

    /// One line per source for the connection-status tooltip: the local daemon
    /// (when enabled) plus each daemon the relay reports. Falls back to the
    /// single-source tooltip when nothing is configured.
    var connectionTooltip: String {
        var lines: [String] = []
        if useLocalDaemon {
            lines.append("Local — \(connectionState.shortLabel)")
        }
        if useRelayServer && !relayServerURL.isEmpty {
            if relayConnectionState == .connected && !relayDaemons.isEmpty {
                for label in relayDaemons.values.sorted() {
                    lines.append("\(label) — connected")
                }
            } else {
                lines.append("\(relayServerURL) — \(relayConnectionState.shortLabel)")
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

    func collectSessionIds(from group: AgentGroup) -> [String] {
        let direct = (group.agents ?? []).map(\.id)
        let children = (group.agents ?? []).flatMap { $0.children?.map(\.id) ?? [] }
        let nested = (group.groups ?? []).flatMap { collectSessionIds(from: $0) }
        return direct + children + nested
    }

    /// Recursively flatten agents from a group and its sub-groups.
    private func flattenAgents(from group: AgentGroup) -> [SessionState] {
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

    /// Starts periodic re-hydration so group-level cost values (which arrive
    /// as part of /api/v1/sessions and are not pushed via WebSocket deltas)
    /// stay fresh. Idempotent.
    func startProjectCostsPolling() {
        projectCostsTimer?.invalidate()
        projectCostsTimer = Timer.scheduledTimer(withTimeInterval: projectCostsRefreshInterval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                await self?.hydrateSessions()
            }
        }
    }

    /// Selects which granularity the history bar renders (1, 10, or 60 s).
    /// All three streams arrive continuously over the WebSocket, so this is a
    /// constant-time mirror — no polling kick-off, no cancellation needed.
    func setHistoryGranularity(_ granularitySec: Int) {
        guard [1, 10, 60].contains(granularitySec) else { return }
        currentHistoryGranularitySec = granularitySec
        refreshActiveStateHistory()
    }

    private func refreshActiveStateHistory() {
        let active = historyByGranularity[currentHistoryGranularitySec] ?? [:]
        // Strip leading no-data buckets so HistoryBarView's right-anchored
        // rendering leaves the front of the bar empty (matches pre-WS shape).
        stateHistory = active.mapValues { trimLeadingNoData($0) }
    }

    private func trimLeadingNoData(_ buckets: [String]) -> [String] {
        var i = 0
        while i < buckets.count && buckets[i].isEmpty { i += 1 }
        return Array(buckets[i...])
    }

    /// Maps the wire 2-bit priority code back to its state name.
    /// `""` represents no-data; HistoryBarView treats it as a blank slot.
    private func historyPriorityToState(_ p: Int8) -> String {
        switch p {
        case 0: return "ready"
        case 1: return "working"
        case 2: return "waiting"
        default: return ""
        }
    }

    private func historyPriorityForState(_ s: String) -> Int {
        switch s {
        case "waiting": return 2
        case "working": return 1
        case "ready":   return 0
        default:        return -1 // no-data — strictly less than any real priority
        }
    }

    /// Decodes a 20-char base64 (15 bytes, MSB-first 2-bit codes) into a
    /// 60-element oldest→newest state-name array. Returns nil on malformed
    /// input so the caller can drop the message rather than corrupt the ring.
    private func decodeHistoryBuckets(_ encoded: String) -> [String]? {
        guard let raw = Data(base64Encoded: encoded), raw.count == 15 else { return nil }
        var out: [String] = []
        out.reserveCapacity(60)
        for byte in raw {
            for shift in stride(from: 6, through: 0, by: -2) {
                let code = Int8((byte >> UInt8(shift)) & 0x3)
                out.append(historyPriorityToState(code))
            }
        }
        return out
    }

    private func applyHistorySnapshot(sessionID: String, history: [String: String], generations: [String: UInt64]?) {
        for (granKey, b64) in history {
            guard let gran = Int(granKey),
                  [1, 10, 60].contains(gran),
                  let buckets = decodeHistoryBuckets(b64) else { continue }
            historyByGranularity[gran, default: [:]][sessionID] = buckets
        }
        // Seed the dedup high-water-mark from the snapshot's generations so
        // any tick already reflected in this snapshot gets skipped on arrival.
        if let gens = generations {
            var perGran = lastTickGen[sessionID] ?? [:]
            for (granKey, gen) in gens {
                if let gran = Int(granKey), [1, 10, 60].contains(gran) {
                    perGran[gran] = gen
                }
            }
            lastTickGen[sessionID] = perGran
        }
        refreshActiveStateHistory()
    }

    private func applyHistoryTick(granularitySec: Int, buckets: [String: Int8], bucketGenerations: [String: UInt64]?) {
        guard [1, 10, 60].contains(granularitySec) else { return }
        var dict = historyByGranularity[granularitySec] ?? [:]
        var changedActive = false
        for (sid, prio) in buckets {
            // Skip if this tick has already been folded into our snapshot.
            if let gen = bucketGenerations?[sid] {
                let last = lastTickGen[sid]?[granularitySec] ?? 0
                if gen <= last { continue }
                var perGran = lastTickGen[sid] ?? [:]
                perGran[granularitySec] = gen
                lastTickGen[sid] = perGran
            }
            var arr = dict[sid] ?? Array(repeating: "", count: 60)
            if arr.count == 60 { arr.removeFirst() }
            arr.append(historyPriorityToState(prio))
            // Pad to 60 if a previously-unknown session ticks before its snapshot.
            while arr.count < 60 { arr.insert("", at: 0) }
            dict[sid] = arr
            changedActive = true
        }
        historyByGranularity[granularitySec] = dict
        if changedActive && granularitySec == currentHistoryGranularitySec {
            refreshActiveStateHistory()
        }
    }

    private func applyHistoryUpgrade(sessionID: String, priority: Int8) {
        let newState = historyPriorityToState(priority)
        let newPrio = historyPriorityForState(newState)
        var changedActive = false
        for gran in [1, 10, 60] {
            var dict = historyByGranularity[gran] ?? [:]
            guard var arr = dict[sessionID], !arr.isEmpty else { continue }
            let lastPrio = historyPriorityForState(arr[arr.count - 1])
            if newPrio > lastPrio {
                arr[arr.count - 1] = newState
                dict[sessionID] = arr
                historyByGranularity[gran] = dict
                if gran == currentHistoryGranularitySec { changedActive = true }
            }
        }
        if changedActive {
            refreshActiveStateHistory()
        }
    }

    /// Fetches the daemon's adapter branding registry into `AgentRegistry.byName`.
    /// Called once per (re)connect from `connect()` — there's no periodic
    /// refresh because adapter rollouts require a daemon restart, which
    /// drops the WebSocket and triggers a reconnect anyway, which calls us.
    /// If a future change ships hot-loadable adapters, add a refresh hook
    /// (or push the registry over the WebSocket).
    private func hydrateAgents() async {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/agents") else { return }
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            let entries = try JSONDecoder().decode([AgentBranding].self, from: data)
            AgentRegistry.byName = Dictionary(uniqueKeysWithValues: entries.map { ($0.name, $0) })
            print("💧 Hydrated \(entries.count) agent brandings from REST API")
        } catch {
            print("💧 Agent branding hydration failed: \(error.localizedDescription)")
        }
    }

    private func hydrateSessions() async {
        // The Local source feeds sessionMap/localApiGroups from the local
        // daemon's REST API. When Local is disabled this must not run, or the
        // 30s cost-poll timer (and any pending rehydration) would silently
        // re-add the local sessions that disabling the source just cleared.
        guard useLocalDaemon else { return }
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/sessions") else { return }
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
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

    private struct WsEnvelope: Decodable {
        let type: String
        let session: SessionState?

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

        enum CodingKeys: String, CodingKey {
            case type
            case session
            case sessionID         = "session_id"
            case history
            case granularitySec    = "granularity_sec"
            case buckets
            case priority
            case generations
            case bucketGenerations = "bucket_generations"
        }
    }

    private func handleWsMessage(_ text: String) {
        guard let data = text.data(using: .utf8) else { return }
        do {
            let envelope = try JSONDecoder().decode(WsEnvelope.self, from: data)
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
                    rebuildSessionsFromMap()
                    if isDebugMode {
                        let cost = session.metrics?.estimatedCostUSD ?? 0
                        let ctx = session.metrics?.contextUtilization ?? 0
                        let inGroups = groupedSessionIds.contains(session.id)
                        print("📨 session_updated id=\(session.id) state=\(session.state.rawValue) cost=\(cost) ctx=\(ctx) inGroupedIds=\(inGroups)")
                    }
                    patchApiGroups(session: session)
                    if let old = oldState, old != session.state {
                        checkStateTransitionNotification(session: session, previousState: old)
                    }
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
            default:
                break
            }
        } catch {
            print("⚠️ Failed to decode WS message: \(error.localizedDescription)")
        }
    }

    /// Set of session IDs present in apiGroups (for fast membership check).
    var groupedSessionIds = Set<String>()

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

    private func groupContains(_ group: AgentGroup, sessionId: String) -> Bool {
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

    private var rehydrationTask: Task<Void, Never>?

    /// Schedule a debounced re-hydration (for structural changes like session deletion).
    private func scheduleRehydration() {
        rehydrationTask?.cancel()
        rehydrationTask = Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 0.5s debounce
            guard !Task.isCancelled else { return }
            await hydrateSessions()
        }
    }

    private func rebuildSessionsFromMap() {
        // Merge relay-only sessions (ids not present locally) so the cycle and
        // menu-bar counts include other machines' sessions. Local wins on id
        // collision — the same daemon reached via both sources shows once.
        let localIDs = Set(sessionMap.keys)
        // Two different relay daemons sharing a session_id stay distinct (keyed
        // by compound rowID), so build a flat array rather than a bare-id dict
        // — a dict would collide them back into one row (#537).
        let relayOnly = relaySessionMap.values.filter { !localIDs.contains($0.id) }
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

    // MARK: - Computed Properties for UI

    var glyphStrip: String {
        if sessions.isEmpty {
            return "○"  // Empty state indicator
        }

        if sessions.count <= 3 {
            return sessions.map { $0.state.glyph }.joined(separator: " ")
        } else {
            return "\(sessions.count) sessions"
        }
    }

    var hasActiveSessions: Bool {
        sessions.contains { $0.state == .working || $0.state == .waiting }
    }

    var workingSessions: Int {
        sessions.filter { $0.state == .working }.count
    }

    var waitingSessions: Int {
        sessions.filter { $0.state == .waiting }.count
    }

    var readySessions: Int {
        sessions.filter { $0.state == .ready }.count
    }

    // MARK: - Context Pressure Notifications

    private var canUseUserNotifications: Bool {
        guard ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil else {
            return false
        }
        guard Bundle.main.bundleIdentifier != nil else {
            return false
        }
        return Bundle.main.bundleURL.pathExtension == "app"
    }

    private func requestNotificationPermission() {
        guard canUseUserNotifications else {
            print("⚠️ Skipping notification setup outside app bundle")
            return
        }
        let center = UNUserNotificationCenter.current()

        // Register the click-forwarder delegate before the first notification
        // is scheduled, otherwise macOS drops notification taps silently.
        if notificationForwarder == nil {
            let forwarder = NotificationClickForwarder(
                onTap: { [weak self] sessionID in
                    self?.handleNotificationTap(sessionID: sessionID)
                },
                focusMonitor: focusMonitor
            )
            notificationForwarder = forwarder
            center.delegate = forwarder
        }
        center.getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .notDetermined:
                // LSUIElement apps can't show the permission prompt — temporarily
                // become a regular app with a visible window so macOS presents the dialog.
                DispatchQueue.main.async {
                    Self.requestWithTemporaryWindow(center: center)
                }
            case .authorized, .provisional, .ephemeral:
                print("✅ Notification permission already granted")
            case .denied:
                print("⚠️ Notification permission denied — user can re-enable in System Settings")
            @unknown default:
                break
            }
        }
    }

    /// Temporarily becomes a regular app with a visible window so macOS will present
    /// the notification permission dialog. Restores LSUIElement behavior afterwards.
    /// Note: ad-hoc signed dev builds won't get the prompt — macOS silently denies them.
    /// The dialog works correctly with Developer ID / notarized builds (release flow).
    private static func requestWithTemporaryWindow(center: UNUserNotificationCenter) {
        NSApp.setActivationPolicy(.regular)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 340, height: 80),
            styleMask: [.titled],
            backing: .buffered,
            defer: false
        )
        window.title = "Irrlicht — Notification Setup"
        window.center()
        window.isReleasedWhenClosed = false
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        // Give LaunchServices time to register the policy change and window
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) {
            center.requestAuthorization(options: [.alert, .sound]) { granted, error in
                DispatchQueue.main.async {
                    window.close()
                    NSApp.setActivationPolicy(.accessory)
                }
                if granted {
                    print("✅ Notification permission granted")
                } else if let error = error {
                    print("⚠️ Notification permission denied: \(error.localizedDescription)")
                    print("ℹ️ Enable notifications in System Settings → Notifications → Irrlicht")
                } else {
                    print("⚠️ Notification permission denied")
                    print("ℹ️ Enable notifications in System Settings → Notifications → Irrlicht")
                }
            }
        }
    }

    /// Checks active sessions for context utilization crossing 80% or 95% thresholds.
    /// Fires a macOS notification the first time each threshold is crossed per session.
    private func checkContextPressureAlerts(sessions: [SessionState]) {
        let thresholds = [80, 95]
        for session in sessions {
            guard session.state == .working || session.state == .waiting,
                  let metrics = session.metrics,
                  metrics.contextUtilization > 0 else { continue }

            let utilization = metrics.contextUtilization
            var fired = notifiedThresholds[session.id] ?? Set<Int>()

            for threshold in thresholds where Double(threshold) <= utilization && !fired.contains(threshold) {
                fired.insert(threshold)
                notifiedThresholds[session.id] = fired
                sendContextPressureNotification(session: session, threshold: threshold, utilization: utilization)
            }
        }
    }

    private func sendContextPressureNotification(session: SessionState, threshold: Int, utilization: Double) {
        guard UserDefaults.standard.bool(forKey: NotificationEvent.contextPressure.enabledKey) else { return }
        let label = session.projectName ?? session.shortId
        sendNotification(
            identifier: "irrlicht-context-\(session.id)-\(threshold)",
            title: "Context pressure: \(threshold)% threshold reached",
            body: "\(label) is at \(String(format: "%.1f%%", utilization)) context. Consider switching to a fresh session.",
            sessionID: session.id,
            event: .contextPressure
        )
    }

    // MARK: - State Transition Notifications

    private func checkStateTransitionNotification(session: SessionState, previousState: SessionState.State) {
        // Skip subagent sessions to avoid notification noise
        if session.parentSessionId != nil { return }

        let notifyReady = UserDefaults.standard.bool(forKey: "notifyOnReady")
        let notifyWaiting = UserDefaults.standard.bool(forKey: "notifyOnWaiting")

        let title: String
        let event: NotificationEvent
        switch session.state {
        case .ready where notifyReady && previousState == .working:
            title = "Agent ready"
            event = .ready
        case .waiting where notifyWaiting && previousState == .working:
            title = "Agent waiting for input"
            event = .waiting
        default:
            return
        }

        let label = session.projectName ?? session.shortId
        let branch = session.gitBranch.map { " (\($0))" } ?? ""

        sendNotification(
            identifier: "irrlicht-state-\(session.id)",
            title: title,
            body: "\(label)\(branch)",
            sessionID: session.id,
            event: event
        )
    }

    private func sendNotification(
        identifier: String,
        title: String,
        body: String,
        sessionID: String,
        event: NotificationEvent
    ) {
        guard canUseUserNotifications else { return }
        let choice = Self.choice(for: event)
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = Self.notificationSound(for: choice)
        content.userInfo = [NotificationUserInfoKey.sessionID: sessionID]

        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                print("⚠️ Failed to send notification: \(error.localizedDescription)")
            }
        }

        // willPresent is unreliable for LSUIElement menubar-only apps — macOS
        // skips it when it considers the app not-in-foreground, and Irrlicht
        // sits in that grey zone. Drive speech from here (on @MainActor) so
        // it actually fires for real state transitions. The per-event toggle
        // is the off switch; users who pick a Speak aloud variant have opted in.
        // TTS bypasses UNNotificationContent entirely, so we must also gate on
        // Focus here — otherwise the loudest sound option leaks through DND.
        if let voice = Self.voiceForSpeak(choice: choice, focusActive: focusMonitor.isFocusActive) {
            SoundPlayer.speak(title: title, body: body, voice: voice)
        }
    }

    /// Pure decision helper: returns the voice to speak with when the chosen
    /// SoundChoice is `.speak(_)` AND Focus is not active. Anything else → nil.
    /// Extracted so the Focus-gating branch in `sendNotification` has direct
    /// unit-test coverage without needing to stub `SoundPlayer`.
    nonisolated static func voiceForSpeak(choice: SoundChoice, focusActive: Bool) -> SpokenVoice? {
        if case .speak(let voice) = choice, !focusActive { return voice }
        return nil
    }

    /// Pure: turn a SoundChoice into a UNNotificationSound. `.none` / `.speak`
    /// → nil (no audible alert from the notification center). A `.custom`
    /// choice whose installed file went missing falls back to Ping.
    nonisolated static func notificationSound(for choice: SoundChoice) -> UNNotificationSound? {
        switch choice {
        case .none, .speak:
            return nil
        case .custom(let installedFilename, _):
            let library = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask).first
            let path = library?
                .appendingPathComponent("Sounds")
                .appendingPathComponent(installedFilename).path
            if let path, FileManager.default.fileExists(atPath: path) {
                return UNNotificationSound(named: UNNotificationSoundName(installedFilename))
            }
            return UNNotificationSound(named: UNNotificationSoundName("Ping.aiff"))
        default:
            guard let name = choice.notificationSoundName else { return .default }
            return UNNotificationSound(named: UNNotificationSoundName(name))
        }
    }

    /// Convenience for tests + callers that want the event → sound lookup in
    /// a single hop. Production path uses `choice(for:)` + `notificationSound(for:)`
    /// directly to avoid double-reading UserDefaults.
    nonisolated static func resolveNotificationSound(for event: NotificationEvent) -> UNNotificationSound? {
        notificationSound(for: choice(for: event))
    }

    nonisolated static func choice(for event: NotificationEvent) -> SoundChoice {
        let raw = UserDefaults.standard.string(forKey: event.soundKey) ?? SoundChoice.default.rawValue
        return SoundChoice(rawValue: raw) ?? .default
    }

    /// Invoked by `NotificationClickForwarder` on the main actor when the user
    /// taps a notification. Silently no-ops for unknown IDs (e.g. a stale
    /// notification for a session that's since been deleted).
    fileprivate func handleNotificationTap(sessionID: String) {
        guard let session = sessionMap[sessionID] else { return }
        SessionLauncher.jump(session)
    }

    // MARK: - Session Order Management

    private func loadSessionOrder() {
        do {
            guard FileManager.default.fileExists(atPath: orderFilePath.path) else {
                print("📋 No session order file found, starting with empty order")
                return
            }

            let data = try Data(contentsOf: orderFilePath)
            let orderData = try JSONDecoder().decode(SessionOrderData.self, from: data)
            sessionOrder = orderData.order
            print("📋 Loaded session order with \(sessionOrder.count) sessions")
        } catch {
            print("📋 Failed to load session order: \(error)")
            sessionOrder = []
        }
    }

    private func saveSessionOrder() {
        do {
            let orderData = SessionOrderData(version: 1, order: sessionOrder)
            let data = try JSONEncoder().encode(orderData)
            try data.write(to: orderFilePath)
            print("💾 Saved session order with \(sessionOrder.count) sessions")
        } catch {
            print("💾 Failed to save session order: \(error)")
            lastError = "Failed to save session order: \(error.localizedDescription)"
        }
    }

    private func sortSessionsByOrder(_ sessions: [SessionState]) -> [SessionState] {
        // Key by rowID (compound for relay sessions): two daemons sharing a
        // session_id would otherwise trap `uniqueKeysWithValues` on the dup
        // key (#537). rowID == id for local sessions, so order is unchanged.
        let sessionMap = Dictionary(uniqueKeysWithValues: sessions.map { ($0.rowID, $0) })

        var orderedSessions: [SessionState] = []

        // First, add sessions in the saved order
        for sessionId in sessionOrder {
            if let session = sessionMap[sessionId] {
                orderedSessions.append(session)
            }
        }

        // Then add any new sessions that aren't in the saved order yet
        let orderedIds = Set(sessionOrder)
        let newSessions = sessions.filter { !orderedIds.contains($0.rowID) }

        // Sort new sessions: active first, then by recency (as fallback for new sessions)
        let sortedNewSessions = newSessions.sorted { lhs, rhs in
            if lhs.state == .ready && rhs.state != .ready {
                return false
            } else if lhs.state != .ready && rhs.state == .ready {
                return true
            } else {
                return lhs.updatedAt > rhs.updatedAt
            }
        }

        orderedSessions.append(contentsOf: sortedNewSessions)

        print("🔢 Sorted \(orderedSessions.count) sessions (\(sessionOrder.count) from saved order, \(sortedNewSessions.count) new)")
        return orderedSessions
    }

    private func updateSessionOrder(with sessions: [SessionState]) {
        let currentSessionIds = Set(sessions.map { $0.rowID })
        let orderedSessionIds = sessions.map { $0.rowID }

        // Only update if the order has changed
        if sessionOrder != orderedSessionIds {
            sessionOrder = orderedSessionIds
            saveSessionOrder()
            print("🔄 Updated session order: removed \(Set(sessionOrder).subtracting(currentSessionIds).count), added \(currentSessionIds.subtracting(Set(sessionOrder)).count)")
        }
    }

    func reorderSession(from sourceIndex: Int, to destinationIndex: Int) {
        guard sourceIndex != destinationIndex,
              sourceIndex >= 0, sourceIndex < sessions.count,
              destinationIndex >= 0, destinationIndex <= sessions.count else {
            return
        }

        // Move session in the sessions array
        let session = sessions.remove(at: sourceIndex)
        let adjustedDestination = destinationIndex > sourceIndex ? destinationIndex - 1 : destinationIndex
        sessions.insert(session, at: adjustedDestination)

        // Update the order array to match
        sessionOrder = sessions.map { $0.rowID }
        saveSessionOrder()

        print("🔄 Reordered session \(session.shortId) from \(sourceIndex) to \(adjustedDestination)")
    }

    // MARK: - Project Group Order Management

    private func loadProjectGroupOrder() {
        projectGroupOrder = UserDefaults.standard.stringArray(forKey: projectGroupOrderKey) ?? []
        print("📋 Loaded project group order with \(projectGroupOrder.count) groups")
    }

    private func saveProjectGroupOrder() {
        UserDefaults.standard.set(projectGroupOrder, forKey: projectGroupOrderKey)
        print("💾 Saved project group order with \(projectGroupOrder.count) groups")
    }

    /// Syncs `projectGroupOrder` with incoming names (appends new, drops gone)
    /// and returns the groups sorted by that order.
    private func orderedGroups(_ groups: [AgentGroup]) -> [AgentGroup] {
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

    // MARK: - Duplicate Session Handling

    private func assignDuplicateIndexes(_ sessions: inout [SessionState]) {
        // Group sessions by project/branch combination
        var duplicateGroups: [String: [Int]] = [:]

        for (index, session) in sessions.enumerated() {
            let project = session.projectName ?? "unknown"
            let branch = session.gitBranch ?? "no-git"
            let key = "\(project)/\(branch)"

            if duplicateGroups[key] == nil {
                duplicateGroups[key] = []
            }
            duplicateGroups[key]?.append(index)
        }

        // Assign duplicate indexes for groups with more than one session
        for (_, indices) in duplicateGroups {
            if indices.count > 1 {
                for (duplicateNumber, sessionIndex) in indices.enumerated() {
                    sessions[sessionIndex].duplicateIndex = duplicateNumber + 1
                }
            }
        }
    }

    // MARK: - Session Management Actions

    func resetSessionState(sessionId: String) {
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        do {
            guard let existingData = try? Data(contentsOf: sessionFilePath),
                  let existingJson = try? JSONSerialization.jsonObject(with: existingData) as? [String: Any] else {
                lastError = "Failed to load session data for reset"
                return
            }
            var updatedJson = existingJson
            updatedJson["state"] = "ready"
            updatedJson["updated_at"] = Int64(Date().timeIntervalSince1970)
            let updatedData = try JSONSerialization.data(withJSONObject: updatedJson, options: [])
            try updatedData.write(to: sessionFilePath)
            if let s = sessionMap[sessionId]?.withState(.ready) {
                sessionMap[sessionId] = s
                rebuildSessionsFromMap()
                patchApiGroups(session: s)
            }
        } catch {
            lastError = "Failed to reset session: \(error.localizedDescription)"
        }
    }

    /// `scheduleRehydration` is intentionally not called — we're authoritative
    /// for the local delete, and a rehydrate could re-add the row before the
    /// daemon's file watcher catches up.
    func deleteSession(sessionId: String) {
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        if FileManager.default.fileExists(atPath: sessionFilePath.path) {
            do {
                try FileManager.default.removeItem(at: sessionFilePath)
            } catch {
                lastError = "Failed to delete session: \(error.localizedDescription)"
                return
            }
        }
        notifiedThresholds.removeValue(forKey: sessionId)
        purgeSessionState(sessionId: sessionId)
        saveSessionOrder()
    }

    /// Caller persists `sessionOrder` if it's authoritative for the deletion;
    /// the WS handler skips the save because the daemon is the source of truth
    /// in that path and will re-emit on reconnect.
    private func purgeSessionState(sessionId: String) {
        sessionMap.removeValue(forKey: sessionId)
        sessionOrder.removeAll { $0 == sessionId }
        lastTickGen.removeValue(forKey: sessionId)
        for gran in [1, 10, 60] {
            historyByGranularity[gran]?.removeValue(forKey: sessionId)
        }
        rebuildSessionsFromMap()
        removeFromApiGroups(sessionId: sessionId)
    }

    // MARK: - Debug State Dump (IRRLICHT_DEBUG=1)

    private var isDebugMode: Bool {
        ProcessInfo.processInfo.environment["IRRLICHT_DEBUG"] == "1"
    }

    /// Writes current session state to ~/.irrlicht/debug-state.json when IRRLICHT_DEBUG=1.
    /// Agents can verify UI state with: cat ~/.irrlicht/debug-state.json
    private func writeDebugState() {
        guard isDebugMode else { return }

        let debugDir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".irrlicht")
        let debugFile = debugDir.appendingPathComponent("debug-state.json")

        do {
            try FileManager.default.createDirectory(at: debugDir, withIntermediateDirectories: true)

            let entries = sessions.map { session in
                DebugSessionEntry(
                    id: session.id,
                    projectName: session.projectName,
                    state: session.state.rawValue,
                    model: session.effectiveModel,
                    contextUtilization: session.metrics?.contextUtilization ?? 0.0,
                    totalTokens: session.metrics?.totalTokens ?? 0
                )
            }

            // Compute project groups (mirrors SessionListView.projectGroups logic)
            let sessionIds = Set(sessions.map { $0.id })
            let subagentIds: Set<String> = Set(sessions.compactMap { session in
                guard let pid = session.parentSessionId, sessionIds.contains(pid) else { return nil }
                return session.id
            })
            var grouped: [String: [String]] = [:]
            for session in sessions where !subagentIds.contains(session.id) {
                let cwd = session.cwd
                let projectDir: String
                if cwd.isEmpty {
                    projectDir = "unknown"
                } else {
                    let last = URL(fileURLWithPath: cwd).lastPathComponent
                    projectDir = last.isEmpty ? "unknown" : last
                }
                grouped[projectDir, default: []].append(session.id)
            }
            let projectGroups = grouped.map { key, sessionGroupIds in
                DebugProjectGroup(
                    projectDirectory: key,
                    accessibilityIdentifier: "project-group-\(key)",
                    sessionIds: sessionGroupIds.sorted()
                )
            }.sorted { $0.projectDirectory < $1.projectDirectory }

            let formatter = ISO8601DateFormatter()
            let debugState = DebugState(
                sessions: entries,
                projectGroups: projectGroups,
                sessionCount: sessions.count,
                workingCount: workingSessions,
                waitingCount: waitingSessions,
                readyCount: readySessions,
                lastUpdated: formatter.string(from: Date())
            )

            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let data = try encoder.encode(debugState)
            try data.write(to: debugFile, options: .atomic)
        } catch {
            print("⚠️ Failed to write debug state: \(error.localizedDescription)")
        }
    }
}

// MARK: - Debug State Data Structures

private struct DebugSessionEntry: Codable {
    let id: String
    let projectName: String?
    let state: String
    let model: String
    let contextUtilization: Double
    let totalTokens: Int64
}

private struct DebugProjectGroup: Codable {
    let projectDirectory: String
    let accessibilityIdentifier: String
    let sessionIds: [String]
}

private struct DebugState: Codable {
    let sessions: [DebugSessionEntry]
    let projectGroups: [DebugProjectGroup]
    let sessionCount: Int
    let workingCount: Int
    let waitingCount: Int
    let readyCount: Int
    let lastUpdated: String
}

// MARK: - Supporting Data Structures

private struct SessionOrderData: Codable {
    let version: Int
    let order: [String]
}

/// Keys used in UNNotificationContent.userInfo so notification click-handlers
/// can identify the originating session.
enum NotificationUserInfoKey {
    static let sessionID = "sessionID"
}

/// NSObject-based forwarder that receives `UNUserNotificationCenterDelegate`
/// callbacks and hands them off to a closure on the main actor. Used instead
/// of making `SessionManager` itself inherit from NSObject.
final class NotificationClickForwarder: NSObject, UNUserNotificationCenterDelegate {
    private let onTap: @MainActor (String) -> Void
    private let focusMonitor: FocusStateProviding

    init(onTap: @escaping @MainActor (String) -> Void, focusMonitor: FocusStateProviding) {
        self.onTap = onTap
        self.focusMonitor = focusMonitor
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let sessionID = response.notification.request.content.userInfo[NotificationUserInfoKey.sessionID] as? String
        if let sessionID {
            Task { @MainActor in
                onTap(sessionID)
            }
        }
        completionHandler()
    }

    // Show banners for notifications delivered while the app is foregrounded
    // — without this, in-app notifications are silently suppressed on macOS.
    // Under Focus / DND, suppress both banner and sound: the user has asked
    // macOS for quiet, and forcing `.sound` here is what lets the sound leak
    // through even when the OS is suppressing the banner.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler(Self.presentationOptions(focusActive: focusMonitor.isFocusActive))
    }

    /// Pure decision helper for `willPresent`. Extracted so tests can verify
    /// the Focus-gating branch without needing a real UNUserNotificationCenter
    /// instance (which can't be constructed outside an app bundle).
    nonisolated static func presentationOptions(focusActive: Bool) -> UNNotificationPresentationOptions {
        focusActive ? [] : [.banner, .sound]
    }
}
