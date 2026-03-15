import Foundation
import Combine
import Darwin

@MainActor
class SessionManager: ObservableObject {
    @Published var sessions: [SessionState] = []
    @Published var isWatching: Bool = false
    @Published var lastError: String?
    
    private let instancesPath: URL
    private let orderFilePath: URL
    private var sessionOrder: [String] = []
    private var fileSystemWatcher: DispatchSourceFileSystemObject?
    private var debounceTimer: Timer?
    private var periodicUpdateTimer: Timer?
    private let debounceInterval: TimeInterval = 0.2 // 200ms debounce
    private let updateInterval: TimeInterval = 1.0 // 1 second periodic updates
    
    init() {
        // Initialize instances directory path
        let homeURL = FileManager.default.homeDirectoryForCurrentUser
        let supportPath = homeURL
            .appendingPathComponent("Library")
            .appendingPathComponent("Application Support")
            .appendingPathComponent("Irrlicht")
        
        self.instancesPath = supportPath.appendingPathComponent("instances")
        self.orderFilePath = supportPath.appendingPathComponent("session-order.json")
        
        Task {
            loadSessionOrder()
            startWatching()
            loadExistingSessions()
        }
    }
    
    deinit {
        Task { @MainActor in
            self.stopWatching()
        }
    }
    
    // MARK: - File System Watching
    
    func startWatching() {
        guard !isWatching else { return }
        
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
        
        isWatching = true
    }
    
    func stopWatching() {
        fileSystemWatcher?.cancel()
        fileSystemWatcher = nil
        debounceTimer?.invalidate()
        debounceTimer = nil
        periodicUpdateTimer?.invalidate()
        periodicUpdateTimer = nil
        isWatching = false
    }
    
    private func debouncedReload() {
        debounceTimer?.invalidate()
        debounceTimer = Timer.scheduledTimer(withTimeInterval: debounceInterval, repeats: false) { [weak self] _ in
            Task {
                await self?.loadExistingSessions()
            }
        }
    }
    
    // MARK: - Session Loading
    
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
            
            // Auto-cleanup cancelled_by_user sessions older than 30 seconds.
            // These are terminal (no more events will fire) so they self-expire.
            let cutoff = Date().addingTimeInterval(-30)
            var cancelledIds: [String] = []
            for session in newSessions where session.state == .cancelledByUser {
                if session.updatedAt < cutoff {
                    let filePath = instancesPath.appendingPathComponent("\(session.id).json")
                    try? FileManager.default.removeItem(at: filePath)
                    cancelledIds.append(session.id)
                    print("🧹 Auto-deleted cancelled_by_user session \(session.shortId)")
                }
            }
            if !cancelledIds.isEmpty {
                newSessions.removeAll { cancelledIds.contains($0.id) }
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

            // TTL-based expiry for ready sessions.
            // sessionTTLMinutes: 0 = never expire, >0 = expire after N minutes.
            // Default is 30 when the key hasn't been set yet.
            let effectiveTTL: Int = UserDefaults.standard.object(forKey: "sessionTTLMinutes") == nil
                ? 30
                : UserDefaults.standard.integer(forKey: "sessionTTLMinutes")
            if effectiveTTL > 0 {
                let ttlCutoff = Date().addingTimeInterval(-Double(effectiveTTL) * 60)
                var expiredIds: [String] = []
                for session in newSessions where session.state == .ready {
                    if session.updatedAt < ttlCutoff {
                        let filePath = instancesPath.appendingPathComponent("\(session.id).json")
                        try? FileManager.default.removeItem(at: filePath)
                        expiredIds.append(session.id)
                        print("⏰ TTL-expired ready session \(session.shortId) (idle > \(effectiveTTL)m)")
                    }
                }
                if !expiredIds.isEmpty {
                    newSessions.removeAll { expiredIds.contains($0.id) }
                }
            }

            // Sort sessions according to saved order, with new sessions at the end
            newSessions = sortSessionsByOrder(newSessions)

            // Update session order to include any new sessions and remove deleted ones
            updateSessionOrder(with: newSessions)
            
            // Assign duplicate indexes for sessions with same project/branch
            assignDuplicateIndexes(&newSessions)
            
            sessions = newSessions
            lastError = nil
            
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
            
        } catch {
            print("❌ Failed to reset session state: \(error)")
            lastError = "Failed to reset session: \(error.localizedDescription)"
        }
    }
    
    func deleteSession(sessionId: String) {
        print("🗑️ Deleting session \(sessionId)")
        
        let sessionFilePath = instancesPath.appendingPathComponent("\(sessionId).json")
        
        do {
            // Remove session file
            try FileManager.default.removeItem(at: sessionFilePath)
            
            // Remove from session order
            sessionOrder.removeAll { $0 == sessionId }
            saveSessionOrder()
            
            print("✅ Successfully deleted session \(sessionId)")
            
        } catch {
            print("❌ Failed to delete session: \(error)")
            lastError = "Failed to delete session: \(error.localizedDescription)"
        }
    }
}

// MARK: - Supporting Data Structures

private struct SessionOrderData: Codable {
    let version: Int
    let order: [String]
}