import Foundation
import Combine

@MainActor
class SessionManager: ObservableObject {
    @Published var sessions: [SessionState] = []
    @Published var isWatching: Bool = false
    @Published var lastError: String?
    
    private let instancesPath: URL
    private var fileSystemWatcher: DispatchSourceFileSystemObject?
    private var debounceTimer: Timer?
    private var periodicUpdateTimer: Timer?
    private let debounceInterval: TimeInterval = 0.2 // 200ms debounce
    private let updateInterval: TimeInterval = 1.0 // 1 second periodic updates
    private let finishedTTL: TimeInterval = 300 // 5 minutes TTL for finished sessions
    
    init() {
        // Initialize instances directory path
        let homeURL = FileManager.default.homeDirectoryForCurrentUser
        self.instancesPath = homeURL
            .appendingPathComponent("Library")
            .appendingPathComponent("Application Support")
            .appendingPathComponent("Irrlicht")
            .appendingPathComponent("instances")
        
        Task {
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
        do {
            let fileURLs = try FileManager.default.contentsOfDirectory(
                at: instancesPath,
                includingPropertiesForKeys: [.contentModificationDateKey],
                options: [.skipsHiddenFiles]
            ).filter { $0.pathExtension == "json" }
            
            var newSessions: [SessionState] = []
            
            for fileURL in fileURLs {
                do {
                    let data = try Data(contentsOf: fileURL)
                    let session = try JSONDecoder().decode(SessionState.self, from: data)
                    
                    // Skip old finished sessions (TTL cleanup)
                    if session.state == .finished {
                        let age = Date().timeIntervalSince(session.updatedAt)
                        if age > finishedTTL {
                            // Remove old finished session files
                            try? FileManager.default.removeItem(at: fileURL)
                            continue
                        }
                    }
                    
                    newSessions.append(session)
                } catch {
                    print("Failed to decode session file \(fileURL.lastPathComponent): \(error)")
                    // Continue processing other files
                }
            }
            
            // Sort sessions: active first, then by recency
            newSessions.sort { lhs, rhs in
                if lhs.state == .finished && rhs.state != .finished {
                    return false
                } else if lhs.state != .finished && rhs.state == .finished {
                    return true
                } else {
                    return lhs.updatedAt > rhs.updatedAt
                }
            }
            
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
        !sessions.filter { $0.state != .finished }.isEmpty
    }
    
    var workingSessions: Int {
        sessions.filter { $0.state == .working }.count
    }
    
    var waitingSessions: Int {
        sessions.filter { $0.state == .waiting }.count
    }
    
    var finishedSessions: Int {
        sessions.filter { $0.state == .finished }.count
    }
}