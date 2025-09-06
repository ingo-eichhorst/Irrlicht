import Foundation
import os

// Performance metrics from transcript analysis
struct SessionMetrics: Codable {
    let messagesPerMinute: Double   // messages per minute over sliding window
    let elapsedSeconds: Int64       // elapsed time since session start
    let lastMessageAt: Date         // timestamp of last message
    let sessionStartAt: Date        // timestamp of first message/session start
    let totalTokens: Int64          // total token count from transcript (0 if not available)
    let modelName: String           // model name extracted from transcript ("" if not available) 
    let contextUtilization: Double  // context utilization percentage (0-100) (0 if not available)
    let pressureLevel: String       // pressure level: "safe", "caution", "warning", "critical" ("unknown" if not available)
    
    enum CodingKeys: String, CodingKey {
        case messagesPerMinute = "messages_per_minute"
        case elapsedSeconds = "elapsed_seconds"  
        case lastMessageAt = "last_message_at"
        case sessionStartAt = "session_start_at"
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
    
    var formattedMessagesPerMinute: String {
        return String(format: "%.1f/min", messagesPerMinute)
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
        case "safe":
            return "🟢"
        case "caution":
            return "🟡"
        case "warning":
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
        case "safe":
            return "#10B981"   // emerald
        case "caution":
            return "#F59E0B"   // amber
        case "warning":
            return "#EF4444"   // red
        case "critical":
            return "#DC2626"   // dark red
        case "unknown", "":
            return "#6B7280"   // gray
        default:
            return "#6B7280"   // gray
        }
    }
    
    // Check if context utilization data is available
    var hasContextData: Bool {
        return totalTokens > 0 && !modelName.isEmpty && pressureLevel != "unknown" && !pressureLevel.isEmpty
    }
}

struct SessionState: Identifiable, Codable {
    let id: String              // session_id
    let state: State            // working, waiting, finished
    let model: String           // claude-3.7-sonnet, etc.
    let cwd: String             // working directory
    let transcriptPath: String? // path to transcript.jsonl (optional for backwards compatibility)
    let updatedAt: Date         // last modified timestamp
    let eventCount: Int?        // number of events processed (optional)
    let lastEvent: String?      // last hook event type (optional)
    let metrics: SessionMetrics? // performance metrics from transcript analysis (optional)
    
    private static let logger = Logger(subsystem: "com.anthropic.irrlicht", category: "SessionState")
    
    // Custom coding keys to match JSON from irrlicht-hook
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd
        case transcriptPath = "transcript_path"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
        case metrics
    }
    
    // Custom decoder to handle multiple date formats and missing fields
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        
        id = try container.decode(String.self, forKey: .id)
        state = try container.decode(State.self, forKey: .state)
        model = try container.decodeIfPresent(String.self, forKey: .model) ?? "unknown"
        cwd = try container.decodeIfPresent(String.self, forKey: .cwd) ?? ""
        transcriptPath = try container.decodeIfPresent(String.self, forKey: .transcriptPath)
        eventCount = try container.decodeIfPresent(Int.self, forKey: .eventCount)
        lastEvent = try container.decodeIfPresent(String.self, forKey: .lastEvent)
        metrics = try container.decodeIfPresent(SessionMetrics.self, forKey: .metrics)
        
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
    init(id: String, state: State, model: String, cwd: String, transcriptPath: String? = nil, updatedAt: Date, eventCount: Int? = nil, lastEvent: String? = nil, metrics: SessionMetrics? = nil) {
        self.id = id
        self.state = state
        self.model = model
        self.cwd = cwd
        self.transcriptPath = transcriptPath
        self.updatedAt = updatedAt
        self.eventCount = eventCount
        self.lastEvent = lastEvent
        self.metrics = metrics
    }
    
    enum State: String, CaseIterable, Codable {
        case working, waiting, finished
        
        var glyph: String {
            switch self {
            case .working: return "●"
            case .waiting: return "◔" 
            case .finished: return "✓"
            }
        }
        
        var color: String {
            switch self {
            case .working: return "#8B5CF6"   // purple
            case .waiting: return "#F59E0B"   // amber
            case .finished: return "#10B981"  // emerald
            }
        }
        
        var emoji: String {
            switch self {
            case .working: return "🟣"   // purple circle
            case .waiting: return "🟠"   // orange circle
            case .finished: return "🟢"  // green circle
            }
        }
    }
    
    // Computed properties for UI display
    var shortId: String {
        String(id.suffix(6))  // Show last 6 chars of session ID
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
        "\(shortId) · \(state.rawValue) · \(effectiveModel) · \(timeAgo)"
    }
    
    var safeEventCount: Int {
        eventCount ?? 0
    }
}