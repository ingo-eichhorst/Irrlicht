import Foundation
import Combine
import Darwin
import UserNotifications

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

    /// GasTownProvider reference for forwarding gastown_state WebSocket messages.
    weak var gasTownProvider: GasTownProvider?

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

    /// Dashboard response from the unified API endpoint.
    private struct DashboardResponse: Decodable {
        let orchestrator: OrchestratorSummary?
        let groups: [AgentGroup]?
    }

    private struct OrchestratorSummary: Decodable {
        let adapter: String?
        let running: Bool?
        let workUnits: [DashboardWorkUnit]?

        enum CodingKeys: String, CodingKey {
            case adapter, running
            case workUnits = "work_units"
        }
    }

    private struct DashboardWorkUnit: Decodable {
        let id: String
        let type: String
        let name: String
        let source: String
        let total: Int
        let done: Int
    }

    private struct AgentGroup: Decodable {
        let name: String
        let status: String?
        let agents: [SessionState]?
    }

    private func hydrateSessions() async {
        guard let url = URL(string: "http://localhost:7837/api/v1/sessions") else { return }
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
            let decoder = JSONDecoder()
            let dashboard = try decoder.decode(DashboardResponse.self, from: data)

            // Flatten groups → agents → sessions (including children).
            var states: [SessionState] = []
            for group in dashboard.groups ?? [] {
                for agent in group.agents ?? [] {
                    states.append(agent)
                    for child in agent.children ?? [] {
                        states.append(child)
                    }
                }
            }
            sessionMap = Dictionary(uniqueKeysWithValues: states.map { ($0.id, $0) })
            rebuildSessionsFromMap()
            print("💧 Hydrated \(states.count) sessions from REST API")
        } catch {
            print("💧 Hydration failed: \(error.localizedDescription)")
        }
    }

    private struct WsEnvelope: Decodable {
        let type: String
        let session: SessionState?
        let gastown: GasTownState?
        let orchestrator: GasTownState?
    }

    private func handleWsMessage(_ text: String) {
        guard let data = text.data(using: .utf8) else { return }
        do {
            let envelope = try JSONDecoder().decode(WsEnvelope.self, from: data)
            switch envelope.type {
            case "session_created", "session_updated":
                if let session = envelope.session {
                    let oldState = sessionMap[session.id]?.state
                    sessionMap[session.id] = session
                    rebuildSessionsFromMap()
                    if let old = oldState, old != session.state {
                        checkStateTransitionNotification(session: session, from: old)
                    }
                }
            case "session_deleted":
                if let session = envelope.session {
                    sessionMap.removeValue(forKey: session.id)
                    sessionOrder.removeAll { $0 == session.id }
                    rebuildSessionsFromMap()
                }
            case "orchestrator_state":
                if let orchState = envelope.orchestrator {
                    gasTownProvider?.handleWebSocketUpdate(orchState)
                }
            case "gastown_state":
                // Legacy: keep for backward compat during transition.
                if let gtState = envelope.gastown {
                    gasTownProvider?.handleWebSocketUpdate(gtState)
                }
            default:
                print("⚠️ Unknown WS message type: \(envelope.type)")
            }
        } catch {
            print("⚠️ Failed to decode WS message: \(error.localizedDescription)")
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
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, error in
            if granted {
                print("✅ Notification permission granted")
            } else if let error = error {
                print("⚠️ Notification permission error: \(error.localizedDescription)")
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
        guard canUseUserNotifications else { return }
        let content = UNMutableNotificationContent()
        content.title = "Context pressure: \(threshold)% threshold reached"
        let label = session.projectName ?? session.shortId
        content.body = "\(label) is at \(String(format: "%.1f%%", utilization)) context. Consider switching to a fresh session."
        content.sound = .default

        let request = UNNotificationRequest(
            identifier: "irrlicht-context-\(session.id)-\(threshold)",
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                print("⚠️ Failed to send context pressure notification: \(error.localizedDescription)")
            } else {
                print("🔔 Context pressure alert (\(threshold)%) fired for session \(session.shortId)")
            }
        }
    }

    // MARK: - State Transition Notifications

    private func checkStateTransitionNotification(session: SessionState, from oldState: SessionState.State) {
        // Skip subagent sessions to avoid notification noise
        if session.parentSessionId != nil { return }

        guard canUseUserNotifications else { return }

        let notifyReady = UserDefaults.standard.bool(forKey: "notifyOnReady")
        let notifyWaiting = UserDefaults.standard.bool(forKey: "notifyOnWaiting")

        let title: String
        switch session.state {
        case .ready where notifyReady:
            title = "Agent ready"
        case .waiting where notifyWaiting:
            title = "Agent waiting for input"
        default:
            return
        }

        let label = session.projectName ?? session.shortId
        let branch = session.gitBranch.map { " (\($0))" } ?? ""

        let content = UNMutableNotificationContent()
        content.title = title
        content.body = "\(label)\(branch)"
        content.sound = .default

        let timestamp = Int(Date().timeIntervalSince1970 * 1000)
        let request = UNNotificationRequest(
            identifier: "irrlicht-state-\(session.id)-\(timestamp)",
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                print("⚠️ Failed to send state transition notification: \(error.localizedDescription)")
            } else {
                print("🔔 State transition alert (\(session.state.rawValue)) fired for session \(session.shortId)")
            }
        }
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

    func reorderGroup(parentId: String, to destinationGroupIndex: Int) {
        let allSessions = sessions
        let sessionIds = Set(allSessions.map { $0.id })

        // Identify subagent sessions
        let subagentIds: Set<String> = Set(allSessions.compactMap { session in
            guard let pid = session.parentSessionId, sessionIds.contains(pid) else { return nil }
            return session.id
        })

        // Build ordered list of top-level session IDs (one per group)
        var groupParentIds = allSessions.filter { !subagentIds.contains($0.id) }.map { $0.id }

        guard destinationGroupIndex >= 0, destinationGroupIndex <= groupParentIds.count else { return }
        guard let sourceIndex = groupParentIds.firstIndex(of: parentId) else { return }
        guard sourceIndex != destinationGroupIndex else { return }

        // Reorder group parent IDs
        groupParentIds.remove(at: sourceIndex)
        let adjustedDest = destinationGroupIndex > sourceIndex ? destinationGroupIndex - 1 : destinationGroupIndex
        groupParentIds.insert(parentId, at: adjustedDest)

        // Build new flat sessions array from reordered groups (parent followed by subagents)
        let sessionMap = Dictionary(uniqueKeysWithValues: allSessions.map { ($0.id, $0) })
        var newSessions: [SessionState] = []
        for gParentId in groupParentIds {
            guard let parentSession = sessionMap[gParentId] else { continue }
            newSessions.append(parentSession)
            let subagents = allSessions.filter { $0.parentSessionId == gParentId }
            newSessions.append(contentsOf: subagents)
        }

        sessions = newSessions
        sessionOrder = sessions.map { $0.id }
        saveSessionOrder()

        print("🔄 Reordered group \(String(parentId.suffix(6))) from group index \(sourceIndex) to \(adjustedDest)")
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

    func orderedProjectGroups(from groups: [ProjectGroup]) -> [ProjectGroup] {
        let groupMap = Dictionary(uniqueKeysWithValues: groups.map { ($0.projectDirectory, $0) })
        var ordered: [ProjectGroup] = []

        // Groups in saved order first
        for key in projectGroupOrder {
            if let group = groupMap[key] {
                ordered.append(group)
            }
        }

        // New groups not in saved order, sorted alphabetically
        let orderedKeys = Set(projectGroupOrder)
        let newGroups = groups.filter { !orderedKeys.contains($0.projectDirectory) }
            .sorted { $0.displayName < $1.displayName }
        ordered.append(contentsOf: newGroups)

        // Prune stale entries and persist if changed
        let newOrder = ordered.map { $0.projectDirectory }
        if newOrder != projectGroupOrder {
            projectGroupOrder = newOrder
            saveProjectGroupOrder()
        }

        return ordered
    }

    func reorderProjectGroup(projectDirectory: String, to destinationIndex: Int) {
        guard let sourceIndex = projectGroupOrder.firstIndex(of: projectDirectory) else { return }
        guard destinationIndex >= 0, destinationIndex <= projectGroupOrder.count else { return }
        guard sourceIndex != destinationIndex else { return }

        projectGroupOrder.remove(at: sourceIndex)
        let adjusted = destinationIndex > sourceIndex ? destinationIndex - 1 : destinationIndex
        projectGroupOrder.insert(projectDirectory, at: adjusted)
        saveProjectGroupOrder()

        print("🔄 Reordered project group \(projectDirectory) from \(sourceIndex) to \(adjusted)")
    }

    func moveProjectGroupUp(projectDirectory: String) {
        guard let index = projectGroupOrder.firstIndex(of: projectDirectory), index > 0 else { return }
        projectGroupOrder.swapAt(index, index - 1)
        saveProjectGroupOrder()
    }

    func moveProjectGroupDown(projectDirectory: String) {
        guard let index = projectGroupOrder.firstIndex(of: projectDirectory),
              index < projectGroupOrder.count - 1 else { return }
        projectGroupOrder.swapAt(index, index + 1)
        saveProjectGroupOrder()
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
        print("🔄 Resetting session state for \(sessionId) to ready")

        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")

        do {
            // Load existing session data
            guard let existingData = try? Data(contentsOf: sessionFilePath),
                  let existingJson = try? JSONSerialization.jsonObject(with: existingData) as? [String: Any] else {
                print("❌ Failed to load existing session data for reset")
                lastError = "Failed to load session data for reset"
                return
            }

            // Update state to ready and timestamp
            var updatedJson = existingJson
            updatedJson["state"] = "ready"
            updatedJson["updated_at"] = Int64(Date().timeIntervalSince1970)

            // Write back to file
            let updatedData = try JSONSerialization.data(withJSONObject: updatedJson, options: [])
            try updatedData.write(to: sessionFilePath)

            print("✅ Successfully reset session \(sessionId) to ready state")

            // In WebSocket mode, optimistically update the in-memory map so the
            // UI reflects the change immediately (file change will sync to daemon
            // on next daemon restart via SeedFromDisk).
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
            print("❌ Failed to reset session state: \(error)")
            lastError = "Failed to reset session: \(error.localizedDescription)"
        }
    }

    func deleteSession(sessionId: String) {
        print("🗑️ Deleting session \(sessionId)")
        notifiedThresholds.removeValue(forKey: sessionId)

        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")

        // Remove session file if it exists on disk.
        if FileManager.default.fileExists(atPath: sessionFilePath.path) {
            do {
                try FileManager.default.removeItem(at: sessionFilePath)
            } catch {
                print("❌ Failed to delete session file: \(error)")
                lastError = "Failed to delete session: \(error.localizedDescription)"
                return
            }
        }

        // Remove from session order
        sessionOrder.removeAll { $0 == sessionId }
        saveSessionOrder()

        // In WebSocket mode, optimistically update the in-memory map.
        if !useFilePolling {
            sessionMap.removeValue(forKey: sessionId)
            rebuildSessionsFromMap()
        }

        print("✅ Successfully deleted session \(sessionId)")
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
