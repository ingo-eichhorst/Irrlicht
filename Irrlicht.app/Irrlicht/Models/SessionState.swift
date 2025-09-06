import Foundation

struct SessionState: Identifiable, Codable {
    let id: String              // session_id
    let state: State            // working, waiting, finished
    let model: String           // claude-3.7-sonnet, etc.
    let cwd: String             // working directory
    let transcriptPath: String? // path to transcript.jsonl (optional for backwards compatibility)
    let updatedAt: Date         // last modified timestamp
    let eventCount: Int?        // number of events processed (optional)
    let lastEvent: String?      // last hook event type (optional)
    
    // Custom coding keys to match JSON from irrlicht-hook
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd
        case transcriptPath = "transcript_path"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
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
    }
    
    // Regular initializer for testing/preview purposes
    init(id: String, state: State, model: String, cwd: String, transcriptPath: String? = nil, updatedAt: Date, eventCount: Int? = nil, lastEvent: String? = nil) {
        self.id = id
        self.state = state
        self.model = model
        self.cwd = cwd
        self.transcriptPath = transcriptPath
        self.updatedAt = updatedAt
        self.eventCount = eventCount
        self.lastEvent = lastEvent
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
    
    var displayName: String {
        "\(shortId) · \(state.rawValue) · \(model) · \(timeAgo)"
    }
    
    var safeEventCount: Int {
        eventCount ?? 0
    }
}