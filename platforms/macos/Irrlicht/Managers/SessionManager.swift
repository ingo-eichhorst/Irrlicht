import Foundation
import AppKit
import Combine
import Darwin
import SwiftUI

// This type's behavior lives across several `SessionManager+*.swift` files
// (split out in #807 to address a CodeScene hotspot): this file only holds
// shared state, initialization, and teardown.
//
//   SessionManager+WebSocket.swift        — local daemon connection + inbound message pipeline
//   SessionManager+Relay.swift            — Sources reconciliation + relay connection
//   SessionManager+GroupComposition.swift — apiGroups tree (patch/prune/order)
//   SessionManager+History.swift          — history-bar wire decoding
//   SessionManager+Hydration.swift        — REST hydration, consent, backchannel actions
//   SessionManager+Notifications.swift    — context-pressure + state-transition notifications
//   SessionManager+SessionOrdering.swift  — persisted session/duplicate ordering
//   SessionManager+SessionActions.swift   — reset/delete session actions
//   SessionManager+Debug.swift            — IRRLICHT_DEBUG state dump

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

    /// Consent state for the permission wizard (issue #570). Refreshed on
    /// connect and on every `permissions_updated` push — which is also how
    /// the wizard dismisses live when the web dashboard answers first.
    @Published var permissionsSnapshot: PermissionsSnapshot?

    /// All three granularities held in parallel so toggling 1s/10s/60s is
    /// instant — the WebSocket streams every granularity continuously, the
    /// view just picks which dict to mirror into `stateHistory`.
    var historyByGranularity: [Int: [String: [String]]] = [1: [:], 10: [:], 60: [:]]
    /// Per-session per-granularity high-water-mark of applied tick generations.
    /// Lets us drop a tick that's already reflected in our snapshot — closing
    /// the connect-time race where the daemon emits a tick between snapshot
    /// generation and the WebSocket flushing the snapshot to us.
    var lastTickGen: [String: [Int: UInt64]] = [:]
    var currentHistoryGranularitySec: Int = 1

    /// Group names the user has collapsed. Lifted out of GroupView's local
    /// state so (a) SessionListView's size estimator can skip collapsed
    /// groups, and (b) collapse state survives apiGroups re-assignments.
    @Published var collapsedGroupNames: Set<String> = []

    /// Global collapse state for every session's task-summary/question block
    /// (issue #738). A single bool — not a per-id set — so sessions that appear
    /// after a "collapse all" still honor it (a snapshot set would leave new
    /// rows expanded). Default expanded. Persisted in UserDefaults (#799) so
    /// the choice survives app restarts.
    @Published var summariesCollapsed: Bool = UserDefaults.standard.bool(forKey: "summariesCollapsed") {
        didSet { UserDefaults.standard.set(summariesCollapsed, forKey: "summariesCollapsed") }
    }

    /// Set of session IDs present in apiGroups (for fast membership check).
    var groupedSessionIds = Set<String>()

    /// Timer that periodically re-hydrates sessions so group-level cost
    /// values (which only ride the /api/v1/sessions response) stay fresh —
    /// WebSocket deltas only carry individual session updates.
    var projectCostsTimer: Timer?
    let projectCostsRefreshInterval: TimeInterval = 30.0

    /// Daemon-owned directory. `irrlichd` creates it and writes session JSON
    /// files; the app only mutates individual files for explicit user actions
    /// (`resetSessionState`, `deleteSession`). Reads go through the WebSocket.
    let instancesPath: URL
    let orderFilePath: URL
    var sessionOrder: [String] = []

    // Project group ordering (persisted in UserDefaults)
    @Published var projectGroupOrder: [String] = []
    let projectGroupOrderKey = "projectGroupOrder"

    // Tracks which pressure thresholds (80, 95) have already fired a notification
    // for each session ID. Prevents re-firing on every state update.
    var notifiedThresholds: [String: Set<Int>] = [:]

    // MARK: - WebSocket state

    var webSocketTask: URLSessionWebSocketTask?
    var connectTask: Task<Void, Never>?
    var reconnectDelay: TimeInterval = 1.0
    let maxReconnectDelay: TimeInterval = 30.0
    /// Dedicated session for all local-daemon traffic (WebSocket + REST
    /// hydration) instead of `URLSession.shared`. An OS-level connection
    /// cache pinned to one URLSession instance can get stuck against a host
    /// that restarted on the same port, failing forever even once the new
    /// process is healthy (#843) — recreating the instance (see `connect()`)
    /// discards whatever cached state caused that.
    var localURLSession = URLSession(configuration: .ephemeral)
    /// Consecutive local-connect cycles that never got a single byte back
    /// from the daemon. Reset on any confirmed message.
    var consecutiveLocalConnectFailures = 0
    /// After this many consecutive failures, recycle `localURLSession` (#843).
    let localConnectFailuresBeforeSessionRecycle = 3
    /// True once a `localURLSession` recycle hasn't yet been followed by a
    /// confirmed reconnect — surfaces the "this isn't a normal transient
    /// blip anymore" state in the connection tooltip/dot (#843).
    @Published var localConnectionStalled = false
    var sessionMap: [String: SessionState] = [:]
    /// Last stamped push `seq` received on the stream. 0 = fresh cursor
    /// (reset on every (re)connect). See #600.
    var lastPushSeq: UInt64 = 0
    /// Debounced structural re-hydration (session created/deleted, seq gap).
    var rehydrationTask: Task<Void, Never>?

    // MARK: - Coalesced UI refresh state (#690)

    /// Session ids touched since the last flush.
    var pendingDirtySessionIDs = Set<String>()
    /// True while a trailing flush is already scheduled (dedupes timers).
    var uiRefreshScheduled = false
    /// Refresh window. Injectable so tests can flush deterministically.
    var uiRefreshInterval: TimeInterval = 0.1
    /// Count of coalesced flushes performed — test seam proving a burst of N
    /// pushes collapses into far fewer renders.
    var uiRefreshFlushCount = 0

    // MARK: - Relay source (multi-source)
    // A second, optional connection to a standalone irrlichtrelay. It speaks
    // the relay envelope (a `hello` handshake, then `push`-wrapped frames);
    // the local connection above speaks raw daemon frames. Relay sessions are
    // held in their own map so the 30s local re-hydration — which replaces
    // `sessionMap` wholesale — can never drop them. A relay session whose id
    // also exists locally collapses to the local copy on merge, so the same
    // daemon reached over both paths shows once.

    var relayWebSocketTask: URLSessionWebSocketTask?
    var relayConnectTask: Task<Void, Never>?
    var relayReconnectDelay: TimeInterval = 1.0
    @Published var relayConnectionState: ConnectionState = .disconnected
    /// Relay-sourced sessions, keyed by session id.
    var relaySessionMap: [String: SessionState] = [:]
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
    var activeRelayURL: String = ""
    /// Local groups before relay groups are appended. `apiGroups` (published)
    /// is always `orderedGroups(localApiGroups) + relayGroups()`.
    var localApiGroups: [AgentGroup] = []
    /// Last-applied source configuration, so the UserDefaults observer only
    /// reconnects when a Sources setting actually changed.
    var lastSourceConfig: String = ""

    /// GasTownProvider reference for forwarding Gas Town availability.
    weak var gasTownProvider: GasTownProvider? {
        didSet {
            // Re-notify availability in case hydration already ran.
            gasTownProvider?.updateAvailability(hasGasTownGroups)
        }
    }
    /// Tracks whether any group has type == "gastown" from the last hydration.
    var hasGasTownGroups = false

    /// Forwards notification-tap events back to the manager so clicked
    /// notifications can bring the originating terminal/IDE to the front.
    /// Held strongly so UNUserNotificationCenter's weak delegate reference
    /// stays alive.
    var notificationForwarder: NotificationClickForwarder?

    /// Source of truth for "is macOS Focus / DND active right now". Consulted
    /// when emitting notifications so we suppress sound + TTS alongside the
    /// system-suppressed banner. Injectable for tests.
    let focusMonitor: FocusStateProviding

    /// True when running under XCTest. Detected by whether XCTest's runtime
    /// class is loaded into this process — unlike `canUseUserNotifications`'s
    /// `XCTestConfigurationFilePath` env var (only set by Xcode's own test
    /// runner), this also holds for this project's actual test command,
    /// `swift test`, which never sets that variable. Gates real daemon
    /// network activity (WebSocket connect, REST hydration, periodic cost
    /// polling) so unit tests never race against whatever daemon happens to
    /// be reachable on the machine (issue #832).
    let isRunningUnitTests = NSClassFromString("XCTestCase") != nil

    init(focusMonitor: FocusStateProviding = FocusMonitor()) {
        self.focusMonitor = focusMonitor

        let homeURL = FileManager.default.homeDirectoryForCurrentUser
        let supportPath = homeURL
            .appendingPathComponent("Library")
            .appendingPathComponent("Application Support")
            .appendingPathComponent("Irrlicht")

        self.instancesPath = supportPath.appendingPathComponent("instances")
        self.orderFilePath = supportPath.appendingPathComponent("session-order.json")

        // Out-of-the-box defaults. Notifications: all three events disabled —
        // the macOS permission prompt only appears when the user first switches
        // one on in Settings (#425). Sounds stay pre-picked so enabling an
        // event is one click (Ready=Funk, Waiting=Ping, Context=Sosumi).
        // Login item: opt the user in on first launch, with the gate flag
        // tracking that we've applied the default once.
        // register(defaults:) only seeds unset keys, so it never overrides a
        // user who has explicitly picked something else.
        var defaultsSeed: [String: Any] = [
            "launchAtLogin": true,
            "didApplyDefaultLoginItem": false,
            // Sources: local on by default, relay opt-in by URL (mirrors the
            // web dashboard's enableLocalSource / enableRelaySource / relayUrl).
            "useLocalDaemon": true,
            "useRelayServer": false,
            // Publish direction (issue #718): off by default; shares relayServerURL.
            "publishToRelay": false,
            "relayServerURL": "",
            // Context-fill alert threshold (#689): default 80% preserves the
            // historical first-alert point for existing users.
            ContextPressureThreshold.valueKey: ContextPressureThreshold.defaultValue,
            ContextPressureThreshold.unitKey: ContextPressureThreshold.defaultUnit.rawValue,
        ]
        for event in NotificationEvent.allCases {
            defaultsSeed[event.enabledKey] = false
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
            setupNotificationDelegate()
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
}
