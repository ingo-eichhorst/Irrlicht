import AppKit
import Foundation
import os

// Performance metrics from transcript analysis
struct SessionMetrics: Codable {
    let elapsedSeconds: Int64       // elapsed time when metrics were computed (for ready sessions)
    let totalTokens: Int64          // total token count from transcript (0 if not available)
    let modelName: String           // model name extracted from transcript ("" if not available) 
    let contextWindow: Int64?       // model context window size (nil/0 if unknown)
    let contextUtilization: Double  // context utilization percentage (0-100) (0 if not available)
    let pressureLevel: String       // pressure level: "safe", "caution", "warning", "critical" ("unknown" if not available)
    let estimatedCostUSD: Double?   // estimated session cost in USD (nil if not available)
    let lastAssistantText: String?  // last assistant message text, truncated (~200 chars)

    enum CodingKeys: String, CodingKey {
        case elapsedSeconds = "elapsed_seconds"
        case totalTokens = "total_tokens"
        case modelName = "model_name"
        case contextWindow = "context_window"
        case contextUtilization = "context_utilization_percentage"
        case pressureLevel = "pressure_level"
        case estimatedCostUSD = "estimated_cost_usd"
        case lastAssistantText = "last_assistant_text"
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
    
    var formattedTokenUsage: String {
        if totalTokens == 0 { return "—" }
        let used = formattedTokenCount
        if let cw = contextWindow, cw > 0 {
            let window: String
            if cw < 1000 {
                window = "\(cw)"
            } else if cw < 1000000 {
                window = String(format: "%.0fK", Double(cw) / 1000)
            } else {
                window = String(format: "%.0fM", Double(cw) / 1000000)
            }
            return "\(used) / \(window)"
        }
        return "\(used) / ?"
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
            return "#34C759"   // system green
        case "caution":
            return "#FF9500"   // system orange
        case "warning":
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

    var formattedCost: String? {
        guard let cost = estimatedCostUSD, cost > 0 else { return nil }
        if cost < 0.01 { return "<$0.01" }
        if cost < 10 { return String(format: "$%.2f", cost) }
        return String(format: "$%.0f", cost)
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

/// Aggregate state of all child sessions for a parent session.
struct SubagentSummary: Codable {
    let total: Int
    let working: Int
    let waiting: Int
    let ready: Int
}

/// Launcher identifies the terminal or IDE that spawned the session's agent.
/// Captured by the daemon when the PID is first assigned. All fields optional —
/// clients must fall back to the session CWD when nothing identifies the host.
struct Launcher: Codable, Hashable {
    let termProgram: String?
    let itermSessionID: String?
    let termSessionID: String?
    let tmuxPane: String?
    let tmuxSocket: String?
    let vscodePID: Int?
    let tty: String?

    enum CodingKeys: String, CodingKey {
        case termProgram    = "term_program"
        case itermSessionID = "iterm_session_id"
        case termSessionID  = "term_session_id"
        case tmuxPane       = "tmux_pane"
        case tmuxSocket     = "tmux_socket"
        case vscodePID      = "vscode_pid"
        case tty            = "tty"
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
    let lastEvent: String?      // last event type (optional)
    let metrics: SessionMetrics? // performance metrics from transcript analysis (optional)
    let pid: Int?               // Claude Code process PID (optional for backwards compatibility)
    let parentSessionId: String? // parent session ID for subagent sessions (optional)
    let subagents: SubagentSummary? // aggregate state of child sessions (optional)
    let adapter: String?        // source agent: "claude-code", "codex" (optional)
    let daemonVersion: String?  // irrlichd version that created this session (optional)
    var role: String?           // orchestrator role: "witness", "polecat", etc.
    var roleIcon: String?       // orchestrator role emoji
    var roleDescription: String? // orchestrator role description
    var workerName: String?     // orchestrator worker name
    var workerID: String?       // orchestrator worker/bead ID
    let children: [SessionState]? // nested child sessions from API (optional)
    let launcher: Launcher?     // terminal/IDE that spawned this session (optional)

    // For duplicate handling (not stored in JSON, computed by SessionManager)
    var duplicateIndex: Int? = nil

    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionState")

    // Custom coding keys to match JSON from irrlichd
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd, pid
        case gitBranch = "git_branch"
        case projectName = "project_name"
        case transcriptPath = "transcript_path"
        case firstSeen = "first_seen"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
        case metrics
        case parentSessionId = "parent_session_id"
        case subagents
        case adapter
        case daemonVersion = "daemon_version"
        case role
        case roleIcon = "icon"
        case roleDescription = "description"
        case workerName = "worker_name"
        case workerID = "worker_id"
        case children
        case launcher
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
        pid = try container.decodeIfPresent(Int.self, forKey: .pid)
        parentSessionId = try container.decodeIfPresent(String.self, forKey: .parentSessionId)
        subagents = try container.decodeIfPresent(SubagentSummary.self, forKey: .subagents)
        adapter = try container.decodeIfPresent(String.self, forKey: .adapter)
        daemonVersion = try container.decodeIfPresent(String.self, forKey: .daemonVersion)
        role = try container.decodeIfPresent(String.self, forKey: .role)
        roleIcon = try container.decodeIfPresent(String.self, forKey: .roleIcon)
        roleDescription = try container.decodeIfPresent(String.self, forKey: .roleDescription)
        workerName = try container.decodeIfPresent(String.self, forKey: .workerName)
        workerID = try container.decodeIfPresent(String.self, forKey: .workerID)
        children = try container.decodeIfPresent([SessionState].self, forKey: .children)
        launcher = try container.decodeIfPresent(Launcher.self, forKey: .launcher)

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
    init(id: String, state: State, model: String, cwd: String, transcriptPath: String? = nil, gitBranch: String? = nil, projectName: String? = nil, firstSeen: Date, updatedAt: Date, eventCount: Int? = nil, lastEvent: String? = nil, metrics: SessionMetrics? = nil, pid: Int? = nil, parentSessionId: String? = nil, subagents: SubagentSummary? = nil, adapter: String? = nil, daemonVersion: String? = nil, role: String? = nil, roleIcon: String? = nil, roleDescription: String? = nil, workerName: String? = nil, workerID: String? = nil, children: [SessionState]? = nil, launcher: Launcher? = nil) {
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
        self.pid = pid
        self.parentSessionId = parentSessionId
        self.subagents = subagents
        self.adapter = adapter
        self.daemonVersion = daemonVersion
        self.role = role
        self.roleIcon = roleIcon
        self.roleDescription = roleDescription
        self.workerName = workerName
        self.workerID = workerID
        self.children = children
        self.launcher = launcher
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

        /// Hex color without leading `#`, for SVG markup.
        var hexColor: String {
            String(color.dropFirst())
        }

        /// Highest-priority state in a collection (waiting > working > ready).
        static func dominant<C: Collection>(in states: C) -> State where C.Element == State {
            if states.contains(.waiting) { return .waiting }
            if states.contains(.working) { return .working }
            return .ready
        }

        var emoji: String {
            switch self {
            case .working: return "🟣"   // purple circle
            case .waiting: return "🟠"   // orange circle
            case .ready: return "🟢"  // green circle
            }
        }

        var label: String {
            switch self {
            case .working: return "Working"
            case .waiting: return "Waiting for input"
            case .ready: return "Ready"
            }
        }
    }
    
    /// Return a copy preserving role/icon/description from an existing session
    /// when the incoming WS update doesn't carry them.
    func preservingRole(from existing: SessionState) -> SessionState {
        if role != nil { return self }
        var copy = self
        copy.role = existing.role
        copy.roleIcon = existing.roleIcon
        copy.roleDescription = existing.roleDescription
        copy.workerName = existing.workerName
        copy.workerID = existing.workerID
        return copy
    }

    var activeSubagentCount: Int {
        (subagents?.working ?? 0) + (subagents?.waiting ?? 0)
    }

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

    var shortModelName: String {
        var short = effectiveModel.replacingOccurrences(of: "claude-", with: "")
        // "sonnet-4-6" → "sonnet-4.6"
        if let range = short.range(of: #"-(\d+)$"#, options: .regularExpression) {
            short = short.replacingCharacters(in: range, with: "." + short[range].dropFirst())
        }
        return short
    }

    var adapterName: String {
        switch adapter ?? "claude-code" {
        case "codex": return "Codex"
        case "pi": return "Pi"
        default: return "Claude Code"
        }
    }

    /// SVG icon for the adapter (claude-code, codex, etc.)
    var adapterIcon: NSImage? {
        let isDark = NSApp.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        let svg: String
        switch adapter ?? "claude-code" {
        case "claude-code":
            svg = SessionState.claudeCodeSVG
        case "codex":
            svg = SessionState.codexSVG(dark: isDark)
        case "pi":
            svg = SessionState.piSVG(dark: isDark)
        default:
            svg = SessionState.claudeCodeSVG
        }
        guard let data = svg.data(using: .utf8),
              let img = NSImage(data: data) else { return nil }
        img.isTemplate = false
        img.size = NSSize(width: 14, height: 14)
        return img
    }

    // Claude Code mascot — pixel-art rectangular creature with eyes and legs.
    private static let claudeCodeSVG = """
    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">
      <rect x="8" y="4" width="40" height="32" rx="4" fill="#D97757"/>
      <rect x="4" y="16" width="8" height="12" rx="2" fill="#D97757"/>
      <rect x="44" y="16" width="8" height="12" rx="2" fill="#D97757"/>
      <rect x="18" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
      <rect x="30" y="12" width="8" height="8" rx="1" fill="#4A2820"/>
      <rect x="12" y="36" width="6" height="14" rx="1" fill="#D97757"/>
      <rect x="22" y="36" width="6" height="10" rx="1" fill="#D97757"/>
      <rect x="32" y="36" width="6" height="10" rx="1" fill="#D97757"/>
      <rect x="42" y="36" width="6" height="14" rx="1" fill="#D97757"/>
    </svg>
    """

    // Codex — circle with >_ terminal prompt. Color adapts to appearance.
    private static func codexSVG(dark: Bool) -> String {
        let c = dark ? "#E0E0E0" : "#1A1A1A"
        return """
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
          <circle cx="50" cy="50" r="44" fill="none" stroke="\(c)" stroke-width="8"/>
          <path d="M28 38 L42 50 L28 62" fill="none" stroke="\(c)" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
          <line x1="48" y1="62" x2="68" y2="62" stroke="\(c)" stroke-width="7" stroke-linecap="round"/>
        </svg>
        """
    }

    // Pi coding agent — Greek letter pi in a circle. Color adapts to appearance.
    private static func piSVG(dark: Bool) -> String {
        let c = dark ? "#E0E0E0" : "#1A1A1A"
        return """
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
          <circle cx="50" cy="50" r="44" fill="none" stroke="\(c)" stroke-width="8"/>
          <line x1="28" y1="30" x2="72" y2="30" stroke="\(c)" stroke-width="8" stroke-linecap="round"/>
          <line x1="40" y1="30" x2="40" y2="74" stroke="\(c)" stroke-width="8" stroke-linecap="round"/>
          <line x1="60" y1="30" x2="64" y2="74" stroke="\(c)" stroke-width="8" stroke-linecap="round"/>
        </svg>
        """
    }
}