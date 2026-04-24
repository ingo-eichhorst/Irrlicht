import Foundation
import AppKit
import Combine
import Darwin
@preconcurrency import UserNotifications

enum ConnectionState {
    case disconnected   // not started or explicitly stopped
    case connecting     // initial connection attempt
    case connected      // WebSocket active and receiving
    case reconnecting   // transient disconnect, auto-recovering
}

@MainActor
class SessionManager: ObservableObject {
    @Published var sessions: [SessionState] = []
    @Published var allSessions: [SessionState] = [] // includes child sessions for badge counting
    @Published var connectionState: ConnectionState = .disconnected
    @Published var lastError: String?
    @Published var apiGroups: [AgentGroup] = []  // recursive group structure from API
    @Published var stateHistory: [String: [String]] = [:]  // session_id → oldest→newest state strings
    @Published var historyBucketCount: Int = 150

    /// Group names the user has collapsed. Lifted out of GroupView's local
    /// state so (a) SessionListView's size estimator can skip collapsed
    /// groups, and (b) collapse state survives apiGroups re-assignments.
    @Published var collapsedGroupNames: Set<String> = []

    /// Timer that periodically re-hydrates sessions so group-level cost
    /// values (which only ride the /api/v1/sessions response) stay fresh —
    /// WebSocket deltas only carry individual session updates.
    private var projectCostsTimer: Timer?
    private let projectCostsRefreshInterval: TimeInterval = 30.0

    private var historyTimer: Timer?

    private let instancesPath: URL
    private let orderFilePath: URL
    private var sessionOrder: [String] = []

    // Project group ordering (persisted in UserDefaults)
    @Published var projectGroupOrder: [String] = []
    private let projectGroupOrderKey = "projectGroupOrder"

    // Tracks which pressure thresholds (80, 95) have already fired a notification
    // for each session ID. Prevents re-firing on every state update.
    private var notifiedThresholds: [String: Set<Int>] = [:]

    // MARK: - File polling (legacy, active when IRRLICHT_USE_FILES=1)

    private var fileSystemWatcher: DispatchSourceFileSystemObject?
    private var debounceTimer: Timer?
    private var periodicUpdateTimer: Timer?
    private let debounceInterval: TimeInterval = 0.2 // 200ms debounce
    private let updateInterval: TimeInterval = 1.0 // 1 second periodic updates

    // MARK: - WebSocket state

    private var webSocketTask: URLSessionWebSocketTask?
    private var connectTask: Task<Void, Never>?
    private var reconnectDelay: TimeInterval = 1.0
    private let maxReconnectDelay: TimeInterval = 30.0
    private var sessionMap: [String: SessionState] = [:]

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

    // MARK: - Mode selection

    private var useFilePolling: Bool {
        ProcessInfo.processInfo.environment["IRRLICHT_USE_FILES"] == "1"
    }

    init() {
        let homeURL = FileManager.default.homeDirectoryForCurrentUser
        let supportPath = homeURL
            .appendingPathComponent("Library")
            .appendingPathComponent("Application Support")
            .appendingPathComponent("Irrlicht")

        self.instancesPath = supportPath.appendingPathComponent("instances")
        self.orderFilePath = supportPath.appendingPathComponent("session-order.json")

        Task {
            loadSessionOrder()
            loadProjectGroupOrder()
            requestNotificationPermission()
            if self.useFilePolling {
                self.startWatching()
                self.loadExistingSessions()
            } else {
                self.startWebSocket()
            }
            self.startProjectCostsPolling()
        }
    }

    deinit {
        connectTask?.cancel()
        connectTask = nil
        webSocketTask?.cancel(with: .normalClosure, reason: nil)
        webSocketTask = nil
        fileSystemWatcher?.cancel()
        fileSystemWatcher = nil
        debounceTimer?.invalidate()
        debounceTimer = nil
        periodicUpdateTimer?.invalidate()
        periodicUpdateTimer = nil
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
        await hydrateSessions()

        guard let url = URL(string: "ws://localhost:7837/api/v1/sessions/stream") else { return }
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

    private func collectSessionIds(from group: AgentGroup) -> [String] {
        let direct = (group.agents ?? []).map(\.id)
        let nested = (group.groups ?? []).flatMap { collectSessionIds(from: $0) }
        return direct + nested
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

    /// Starts periodic history polling at the specified granularity (1, 10, or 60 s).
    func startHistoryPolling(granularitySec: Int) {
        historyTimer?.invalidate()
        let interval = TimeInterval(max(1, granularitySec))
        Task { @MainActor [weak self] in await self?.fetchHistory(granularitySec: granularitySec) }
        historyTimer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                await self?.fetchHistory(granularitySec: granularitySec)
            }
        }
    }

    func stopHistoryPolling() {
        historyTimer?.invalidate()
        historyTimer = nil
    }

    private func fetchHistory(granularitySec: Int) async {
        guard let url = URL(string: "http://localhost:7837/api/v1/sessions/history?granularity=\(granularitySec)") else { return }
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            struct HistoryResponse: Decodable {
                let sessions: [String: [String]]
                let bucketCount: Int
            }
            let decoder = JSONDecoder()
            decoder.keyDecodingStrategy = .convertFromSnakeCase
            let decoded = try decoder.decode(HistoryResponse.self, from: data)
            stateHistory = decoded.sessions
            historyBucketCount = decoded.bucketCount
        } catch {
            // Non-fatal: history is optional.
        }
    }

    private func hydrateSessions() async {
        guard let url = URL(string: "http://localhost:7837/api/v1/sessions") else { return }
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            let decoder = JSONDecoder()
            let topGroups = try decoder.decode([AgentGroup].self, from: data)

            apiGroups = orderedGroups(topGroups)
            groupedSessionIds = Set(topGroups.flatMap { collectSessionIds(from: $0) })

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
                    patchApiGroups(session: session)
                    if let old = oldState, old != session.state {
                        checkStateTransitionNotification(session: session, previousState: old)
                    }
                }
            case "session_deleted":
                if let session = envelope.session {
                    sessionMap.removeValue(forKey: session.id)
                    sessionOrder.removeAll { $0 == session.id }
                    rebuildSessionsFromMap()
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
            default:
                break
            }
        } catch {
            print("⚠️ Failed to decode WS message: \(error.localizedDescription)")
        }
    }

    /// Set of session IDs present in apiGroups (for fast membership check).
    private var groupedSessionIds = Set<String>()

    /// Patch a session in-place within apiGroups so the list view updates reactively.
    private func patchApiGroups(session: SessionState) {
        guard groupedSessionIds.contains(session.id) else { return }
        apiGroups = apiGroups.map { patchGroup($0, with: session) }
    }

    private func patchGroup(_ group: AgentGroup, with session: SessionState) -> AgentGroup {
        let hasMatch = group.agents?.contains { $0.id == session.id } ?? false
        let hasNestedMatch = group.groups?.contains { groupContains($0, sessionId: session.id) } ?? false
        guard hasMatch || hasNestedMatch else { return group }

        let patchedAgents = hasMatch ? group.agents?.map { $0.id == session.id ? session : $0 } : group.agents
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
        return group.groups?.contains { groupContains($0, sessionId: sessionId) } ?? false
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
        let all = Array(sessionMap.values)
        let ids = Set(all.map { $0.id })

        // Exclude child sessions (subagents) from the main session list
        // so they don't appear in the cycle or as separate rows.
        var topLevel = all.filter { session in
            guard let pid = session.parentSessionId else { return true }
            return !ids.contains(pid)
        }
        topLevel = sortSessionsByOrder(topLevel)
        updateSessionOrder(with: topLevel)
        assignDuplicateIndexes(&topLevel)
        sessions = topLevel
        allSessions = all // includes children for badge counting
        checkContextPressureAlerts(sessions: topLevel)
        writeDebugState()
    }

    // MARK: - File System Watching (legacy)

    func startWatching() {
        guard connectionState == .disconnected else { return }

        // Create directory if it doesn't exist
        createInstancesDirectoryIfNeeded()

        // Set up file system watcher
        let fileDescriptor = open(instancesPath.path, O_EVTONLY)
        guard fileDescriptor >= 0 else {
            lastError = "Failed to open instances directory for watching"
            return
        }

        fileSystemWatcher = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: fileDescriptor,
            eventMask: [.write, .delete, .rename],
            queue: DispatchQueue.main
        )

        fileSystemWatcher?.setEventHandler { [weak self] in
            self?.debouncedReload()
        }

        fileSystemWatcher?.setCancelHandler {
            close(fileDescriptor)
        }

        fileSystemWatcher?.resume()

        // Start periodic update timer
        periodicUpdateTimer = Timer.scheduledTimer(withTimeInterval: updateInterval, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.loadExistingSessions()
            }
        }

        connectionState = .connected
    }

    func stopWatching() {
        fileSystemWatcher?.cancel()
        fileSystemWatcher = nil
        debounceTimer?.invalidate()
        debounceTimer = nil
        periodicUpdateTimer?.invalidate()
        periodicUpdateTimer = nil
        connectionState = .disconnected
    }

    private func debouncedReload() {
        debounceTimer?.invalidate()
        debounceTimer = Timer.scheduledTimer(withTimeInterval: debounceInterval, repeats: false) { [weak self] _ in
            Task {
                await self?.loadExistingSessions()
            }
        }
    }

    // MARK: - Session Loading (file polling mode)

    func loadExistingSessions() {
        print("📂 Loading sessions from: \(instancesPath.path)")
        do {
            let fileURLs = try FileManager.default.contentsOfDirectory(
                at: instancesPath,
                includingPropertiesForKeys: [.contentModificationDateKey],
                options: [.skipsHiddenFiles]
            ).filter { $0.pathExtension == "json" }

            print("📄 Found \(fileURLs.count) session files")

            var newSessions: [SessionState] = []

            for fileURL in fileURLs {
                do {
                    let data = try Data(contentsOf: fileURL)
                    let session = try JSONDecoder().decode(SessionState.self, from: data)

                    newSessions.append(session)
                } catch {
                    print("Failed to decode session file \(fileURL.lastPathComponent): \(error)")
                    // Continue processing other files
                }
            }

            // Auto-cleanup orphaned sessions whose Claude Code process has exited.
            // This catches the common case where Claude Code is force-quit or crashes
            // without firing SessionEnd.
            var orphanedIds: [String] = []
            let staleTTL: TimeInterval = 3600 // 1 hour — for legacy sessions without PID
            for session in newSessions {
                let isOrphaned: Bool
                if let pid = session.pid, pid > 0 {
                    // PID-based check: probe with signal 0 (no signal sent, just liveness check)
                    isOrphaned = kill(pid_t(pid), 0) != 0
                } else {
                    // Legacy session: no PID stored; only reap active states after TTL
                    let isActive = session.state == .working || session.state == .waiting
                    isOrphaned = isActive && session.updatedAt < Date().addingTimeInterval(-staleTTL)
                }
                if isOrphaned {
                    let filePath = instancesPath.appendingPathComponent("\(session.id).json")
                    try? FileManager.default.removeItem(at: filePath)
                    orphanedIds.append(session.id)
                    let pidDesc = session.pid.map { "pid=\($0)" } ?? "no-pid"
                    print("🧹 Auto-deleted orphaned session \(session.shortId) (\(pidDesc))")
                }
            }
            if !orphanedIds.isEmpty {
                newSessions.removeAll { orphanedIds.contains($0.id) }
            }

            // Sort sessions according to saved order, with new sessions at the end
            newSessions = sortSessionsByOrder(newSessions)

            // Update session order to include any new sessions and remove deleted ones
            updateSessionOrder(with: newSessions)

            // Assign duplicate indexes for sessions with same project/branch
            assignDuplicateIndexes(&newSessions)

            sessions = newSessions
            checkContextPressureAlerts(sessions: newSessions)
            writeDebugState()

        } catch {
            lastError = "Failed to load sessions: \(error.localizedDescription)"
            print("Session loading error: \(error)")
        }
    }

    private func createInstancesDirectoryIfNeeded() {
        do {
            try FileManager.default.createDirectory(
                at: instancesPath,
                withIntermediateDirectories: true,
                attributes: nil
            )
        } catch {
            lastError = "Failed to create instances directory: \(error.localizedDescription)"
        }
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
            let forwarder = NotificationClickForwarder { [weak self] sessionID in
                self?.handleNotificationTap(sessionID: sessionID)
            }
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
        let label = session.projectName ?? session.shortId
        sendNotification(
            identifier: "irrlicht-context-\(session.id)-\(threshold)",
            title: "Context pressure: \(threshold)% threshold reached",
            body: "\(label) is at \(String(format: "%.1f%%", utilization)) context. Consider switching to a fresh session.",
            sessionID: session.id
        )
    }

    // MARK: - State Transition Notifications

    private func checkStateTransitionNotification(session: SessionState, previousState: SessionState.State) {
        // Skip subagent sessions to avoid notification noise
        if session.parentSessionId != nil { return }

        let notifyReady = UserDefaults.standard.bool(forKey: "notifyOnReady")
        let notifyWaiting = UserDefaults.standard.bool(forKey: "notifyOnWaiting")

        let title: String
        switch session.state {
        case .ready where notifyReady && previousState == .working:
            title = "Agent ready"
        case .waiting where notifyWaiting && previousState == .working:
            title = "Agent waiting for input"
        default:
            return
        }

        let label = session.projectName ?? session.shortId
        let branch = session.gitBranch.map { " (\($0))" } ?? ""

        sendNotification(
            identifier: "irrlicht-state-\(session.id)",
            title: title,
            body: "\(label)\(branch)",
            sessionID: session.id
        )
    }

    private func sendNotification(identifier: String, title: String, body: String, sessionID: String) {
        guard canUseUserNotifications else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = .default
        // Round-trip the session ID so the click-forwarder can look up
        // the session and jump back to its launching terminal/IDE.
        content.userInfo = [NotificationUserInfoKey.sessionID: sessionID]

        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                print("⚠️ Failed to send notification: \(error.localizedDescription)")
            }
        }
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
        // Create a map of session ID to session for quick lookup
        let sessionMap = Dictionary(uniqueKeysWithValues: sessions.map { ($0.id, $0) })

        var orderedSessions: [SessionState] = []

        // First, add sessions in the saved order
        for sessionId in sessionOrder {
            if let session = sessionMap[sessionId] {
                orderedSessions.append(session)
            }
        }

        // Then add any new sessions that aren't in the saved order yet
        let orderedIds = Set(sessionOrder)
        let newSessions = sessions.filter { !orderedIds.contains($0.id) }

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
        let currentSessionIds = Set(sessions.map { $0.id })
        let orderedSessionIds = sessions.map { $0.id }

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
        sessionOrder = sessions.map { $0.id }
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
        apiGroups = orderedGroups(apiGroups)
    }

    func moveProjectGroupDown(name: String) {
        guard let i = projectGroupOrder.firstIndex(of: name),
              i < projectGroupOrder.count - 1 else { return }
        projectGroupOrder.swapAt(i, i + 1)
        saveProjectGroupOrder()
        apiGroups = orderedGroups(apiGroups)
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
            if !useFilePolling, var s = sessionMap[sessionId] {
                s = SessionState(
                    id: s.id, state: .ready, model: s.model, cwd: s.cwd,
                    transcriptPath: s.transcriptPath, gitBranch: s.gitBranch,
                    projectName: s.projectName, firstSeen: s.firstSeen,
                    updatedAt: Date(), eventCount: s.eventCount,
                    lastEvent: s.lastEvent, metrics: s.metrics,
                    pid: s.pid, parentSessionId: s.parentSessionId
                )
                sessionMap[sessionId] = s
                rebuildSessionsFromMap()
            }
        } catch {
            lastError = "Failed to reset session: \(error.localizedDescription)"
        }
    }

    func deleteSession(sessionId: String) {
        notifiedThresholds.removeValue(forKey: sessionId)
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        if FileManager.default.fileExists(atPath: sessionFilePath.path) {
            do {
                try FileManager.default.removeItem(at: sessionFilePath)
            } catch {
                lastError = "Failed to delete session: \(error.localizedDescription)"
                return
            }
        }
        sessionOrder.removeAll { $0 == sessionId }
        saveSessionOrder()
        if !useFilePolling {
            sessionMap.removeValue(forKey: sessionId)
            rebuildSessionsFromMap()
        }
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

    init(onTap: @escaping @MainActor (String) -> Void) {
        self.onTap = onTap
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
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}
