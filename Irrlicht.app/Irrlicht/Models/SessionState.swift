import Foundation
import os

// Performance metrics from transcript analysis
struct SessionMetrics: Codable {
    let elapsedSeconds: Int64       // elapsed time when metrics were computed (for ready sessions)
    let totalTokens: Int64          // total token count from transcript (0 if not available)
    let modelName: String           // model name extracted from transcript ("" if not available) 
    let contextUtilization: Double  // context utilization percentage (0-100) (0 if not available)
    let pressureLevel: String       // pressure level: "low", "medium", "high", "critical" ("unknown" if not available)
    
    enum CodingKeys: String, CodingKey {
        case elapsedSeconds = "elapsed_seconds"
        case totalTokens = "total_tokens"
        case modelName = "model_name"
        case contextUtilization = "context_utilization_percentage"
        case pressureLevel = "pressure_level"
    }
    
    // Computed properties for UI display
    var formattedElapsedTime: String {
        let minutes = elapsedSeconds / 60
        let seconds = elapsedSeconds % 60
        
        if minutes >= 60 {
            let hours = minutes / 60
            let remainingMinutes = minutes % 60
            return String(format: "%dh %dm", hours, remainingMinutes)
        } else if minutes > 0 {
            return String(format: "%dm %ds", minutes, seconds)
        } else {
            return String(format: "%ds", seconds)
        }
    }
    
    var formattedTokenCount: String {
        if totalTokens == 0 { return "—" }
        
        if totalTokens < 1000 {
            return "\(totalTokens)"
        } else if totalTokens < 1000000 {
            return String(format: "%.1fK", Double(totalTokens) / 1000)
        } else {
            return String(format: "%.1fM", Double(totalTokens) / 1000000)
        }
    }
    
    var formattedContextUtilization: String {
        if contextUtilization == 0 && pressureLevel == "unknown" { return "—" }
        return String(format: "%.1f%%", contextUtilization)
    }
    
    var contextPressureIcon: String {
        switch pressureLevel {
        case "low":
            return "🟢"
        case "medium":
            return "🟡"
        case "high":
            return "🔴"
        case "critical":
            return "⚠️"
        case "unknown", "":
            return "❓"
        default:
            return "❓"
        }
    }
    
    var contextPressureColor: String {
        switch pressureLevel {
        case "low":
            return "#34C759"   // system green
        case "medium":
            return "#FF9500"   // system orange
        case "high":
            return "#FF3B30"   // system red
        case "critical":
            return "#D70015"   // darker red
        case "unknown", "":
            return "#8E8E93"   // system gray
        default:
            return "#8E8E93"   // system gray
        }
    }
    
    // Check if context utilization data is available
    var hasContextData: Bool {
        return totalTokens > 0 && !modelName.isEmpty && pressureLevel != "unknown" && !pressureLevel.isEmpty
    }
    
    // Real-time elapsed time for active sessions
    func formattedRealtimeElapsedTime(sessionFirstSeen: Date) -> String {
        let seconds = Int64(Date().timeIntervalSince(sessionFirstSeen))
        let minutes = seconds / 60
        let remainingSeconds = seconds % 60
        
        if minutes >= 60 {
            let hours = minutes / 60
            let remainingMinutes = minutes % 60
            return String(format: "%dh %dm", hours, remainingMinutes)
        } else if minutes > 0 {
            return String(format: "%dm %ds", minutes, remainingSeconds)
        } else {
            return String(format: "%ds", remainingSeconds)
        }
    }
}

struct SessionState: Identifiable, Codable {
    let id: String              // session_id
    let state: State            // working, waiting, ready
    let model: String           // claude-3.7-sonnet, etc.
    let cwd: String             // working directory
    let transcriptPath: String? // path to transcript.jsonl (optional for backwards compatibility)
    let gitBranch: String?      // git branch name (optional)
    let projectName: String?    // project folder name (optional)
    let firstSeen: Date         // when session was first created
    let updatedAt: Date         // last modified timestamp
    let eventCount: Int?        // number of events processed (optional)
    let lastEvent: String?      // last hook event type (optional)
    let metrics: SessionMetrics? // performance metrics from transcript analysis (optional)
    
    // For duplicate handling (not stored in JSON, computed by SessionManager)
    var duplicateIndex: Int? = nil
    
    private static let logger = Logger(subsystem: "com.anthropic.irrlicht", category: "SessionState")
    
    // Custom coding keys to match JSON from irrlicht-hook
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd
        case gitBranch = "git_branch"
        case projectName = "project_name"
        case transcriptPath = "transcript_path"
        case firstSeen = "first_seen"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
        case metrics
    }
    
    // Custom decoder to handle multiple date formats and missing fields
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        
        id = try container.decode(String.self, forKey: .id)
        // Handle backwards compatibility: "finished" -> "ready"
        let stateString = try container.decode(String.self, forKey: .state)
        if stateString == "finished" {
            state = .ready
        } else {
            state = State(rawValue: stateString) ?? .ready
        }
        model = try container.decodeIfPresent(String.self, forKey: .model) ?? "unknown"
        cwd = try container.decodeIfPresent(String.self, forKey: .cwd) ?? ""
        transcriptPath = try container.decodeIfPresent(String.self, forKey: .transcriptPath)
        gitBranch = try container.decodeIfPresent(String.self, forKey: .gitBranch)
        projectName = try container.decodeIfPresent(String.self, forKey: .projectName)
        eventCount = try container.decodeIfPresent(Int.self, forKey: .eventCount)
        lastEvent = try container.decodeIfPresent(String.self, forKey: .lastEvent)
        metrics = try container.decodeIfPresent(SessionMetrics.self, forKey: .metrics)
        
        // Handle firstSeen date (unix timestamp format)
        if let timestamp = try? container.decode(Double.self, forKey: .firstSeen) {
            firstSeen = Date(timeIntervalSince1970: timestamp)
        } else if let timestamp = try? container.decode(Int.self, forKey: .firstSeen) {
            firstSeen = Date(timeIntervalSince1970: Double(timestamp))
        } else {
            // Fallback to now if no valid date found
            firstSeen = Date()
        }
        
        // Handle multiple date formats
        if let dateString = try? container.decode(String.self, forKey: .updatedAt) {
            // ISO8601 string format
            let formatter = DateFormatter()
            formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'"
            formatter.timeZone = TimeZone(abbreviation: "UTC")
            
            if let date = formatter.date(from: dateString) {
                updatedAt = date
            } else {
                // Fallback to ISO8601 without milliseconds
                formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ss'Z'"
                updatedAt = formatter.date(from: dateString) ?? Date()
            }
        } else if let timestamp = try? container.decode(Double.self, forKey: .updatedAt) {
            // Unix timestamp format
            updatedAt = Date(timeIntervalSince1970: timestamp)
        } else if let timestamp = try? container.decode(Int.self, forKey: .updatedAt) {
            // Unix timestamp as integer
            updatedAt = Date(timeIntervalSince1970: Double(timestamp))
        } else {
            // Default to now if no valid date found
            updatedAt = Date()
        }
        
        // Log session data for debugging (after all properties are set)
        let sessionId = id
        let sessionState = state.rawValue
        let topLevelModel = model
        let metricsModelName = metrics?.modelName ?? "nil"
        let safeEventCount = eventCount ?? 0
        let safeLastEvent = lastEvent ?? "nil"
        print("🔍 Decoded session: id=\(sessionId), state=\(sessionState), topLevelModel=\(topLevelModel), metricsModel=\(metricsModelName), eventCount=\(safeEventCount), lastEvent=\(safeLastEvent)")
    }
    
    // Regular initializer for testing/preview purposes
    init(id: String, state: State, model: String, cwd: String, transcriptPath: String? = nil, gitBranch: String? = nil, projectName: String? = nil, firstSeen: Date, updatedAt: Date, eventCount: Int? = nil, lastEvent: String? = nil, metrics: SessionMetrics? = nil) {
        self.id = id
        self.state = state
        self.model = model
        self.cwd = cwd
        self.transcriptPath = transcriptPath
        self.gitBranch = gitBranch
        self.projectName = projectName
        self.firstSeen = firstSeen
        self.updatedAt = updatedAt
        self.eventCount = eventCount
        self.lastEvent = lastEvent
        self.metrics = metrics
    }
    
    enum State: String, CaseIterable, Codable {
        case working, waiting, ready
        
        var glyph: String {
            switch self {
            case .working: return "hammer.fill"
            case .waiting: return "hourglass" 
            case .ready: return "checkmark.circle.fill"
            }
        }
        
        var color: String {
            switch self {
            case .working: return "#8B5CF6"   // purple to match 🟣
            case .waiting: return "#FF9500"   // system orange  
            case .ready: return "#34C759"  // system green
            }
        }
        
        var emoji: String {
            switch self {
            case .working: return "🟣"   // purple circle
            case .waiting: return "🟠"   // orange circle
            case .ready: return "🟢"  // green circle
            }
        }
    }
    
    // Computed properties for UI display
    var shortId: String {
        String(id.suffix(6))  // Show last 6 chars of session ID
    }
    
    var friendlyName: String {
        // Create user-friendly name from project and branch
        let project = projectName ?? "unknown"
        let branch = gitBranch ?? "no-git"
        let baseName = "\(project)/\(branch)"
        
        // Add duplicate index if needed
        if let index = duplicateIndex, index > 1 {
            return "\(baseName) (\(index))"
        } else {
            return baseName
        }
    }
    
    var timeAgo: String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: updatedAt, relativeTo: Date())
    }
    
    var effectiveModel: String {
        // Prefer metrics.model_name if available, otherwise fall back to top-level model
        let effective = metrics?.modelName.isEmpty == false ? metrics!.modelName : model
        print("🎯 effectiveModel for \(shortId): using '\(effective)' (metrics='\(metrics?.modelName ?? "nil")', topLevel='\(model)')")
        return effective
    }
    
    var displayName: String {
        "\(friendlyName) · \(state.rawValue) · \(effectiveModel) · \(timeAgo)"
    }
    
    var safeEventCount: Int {
        eventCount ?? 0
    }
}