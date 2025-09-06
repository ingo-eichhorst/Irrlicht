import Foundation

struct SessionState: Identifiable, Codable {
    let id: String              // session_id
    let state: State            // working, waiting, finished
    let model: String           // claude-3.7-sonnet, etc.
    let cwd: String             // working directory
    let transcriptPath: String  // path to transcript.jsonl
    let updatedAt: Date         // last modified timestamp
    let eventCount: Int         // number of events processed
    let lastEvent: String       // last hook event type
    
    // Custom coding keys to match JSON from irrlicht-hook
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd
        case transcriptPath = "transcript_path"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
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
}