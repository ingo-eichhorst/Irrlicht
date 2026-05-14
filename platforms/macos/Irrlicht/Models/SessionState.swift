import AppKit
import Foundation
import SwiftUI
import os

/// Branding for one inbound agent adapter, served by the daemon's
/// GET /api/v1/agents endpoint. Co-locating display name + icon SVGs with
/// the Go adapter (issue #260) lets a Go-only contributor add a new adapter
/// without touching Swift — `SessionState.adapterName` / `adapterIcon` look
/// the entry up dynamically.
struct AgentBranding: Decodable {
    let name: String
    let displayName: String
    let iconSVGLight: String
    let iconSVGDark: String

    enum CodingKeys: String, CodingKey {
        case name
        case displayName = "display_name"
        case iconSVGLight = "icon_svg_light"
        case iconSVGDark = "icon_svg_dark"
    }
}

/// File-scope holder for the adapter branding registry. Marked
/// `@MainActor` so the compiler enforces single-threaded access — writes
/// come from `SessionManager.hydrateAgents()` (already on main) and reads
/// come from `SessionState.adapterName` / `adapterIcon` (callers are all
/// SwiftUI views and other MainActor code, so we annotate those properties
/// MainActor too). This is the Swift-6 strict-concurrency-clean shape;
/// it costs nothing under Swift 5 mode and avoids a `nonisolated(unsafe)`
/// hatch later.
@MainActor
enum AgentRegistry {
    static var byName: [String: AgentBranding] = [:]
}

/// A single item in the Claude Code task list, derived from TaskCreate / TaskUpdate tool calls.
struct SessionTask: Codable, Hashable {
    let id: String
    let subject: String
    let description: String?
    let activeForm: String?
    let status: String  // "pending" | "in_progress" | "completed"

    enum CodingKeys: String, CodingKey {
        case id, subject, description, status
        case activeForm = "active_form"
    }

    var isCompleted: Bool { status == "completed" }
    var isInProgress: Bool { status == "in_progress" }

    var displayLabel: String {
        if isInProgress, let form = activeForm, !form.isEmpty {
            return form
        }
        return subject
    }
}

// Performance metrics from transcript analysis
struct SessionMetrics: Codable {
    let elapsedSeconds: Int64       // elapsed time when metrics were computed (for ready sessions)
    let totalTokens: Int64          // total token count from transcript (0 if not available)
    let modelName: String           // model name extracted from transcript ("" if not available)
    let contextWindow: Int64?       // model context window size (nil/0 if unknown)
    let contextUtilization: Double  // context utilization percentage (0-100) (0 if not available)
    let pressureLevel: String       // pressure level: "safe", "caution", "warning", "critical" ("unknown" if not available)
    let contextWindowUnknown: Bool? // true when daemon has no LiteLLM pricing for the model — render tokens-only, no percentage
    let estimatedCostUSD: Double?   // estimated session cost in USD (nil if not available)
    let lastAssistantText: String?  // last assistant message text, truncated (~200 chars)
    let tasks: [SessionTask]?              // Claude Code task list (nil when TaskCreate never called)

    enum CodingKeys: String, CodingKey {
        case elapsedSeconds = "elapsed_seconds"
        case totalTokens = "total_tokens"
        case modelName = "model_name"
        case contextWindow = "context_window"
        case contextUtilization = "context_utilization_percentage"
        case pressureLevel = "pressure_level"
        case contextWindowUnknown = "context_window_unknown"
        case estimatedCostUSD = "estimated_cost_usd"
        case lastAssistantText = "last_assistant_text"
        case tasks
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
    
    var contextPressureColor: Color {
        switch pressureLevel {
        case "safe":     return IrrColors.pressureLow
        case "caution":  return IrrColors.pressureMedium
        case "warning":  return IrrColors.pressureHigh
        case "critical": return IrrColors.pressureCritical
        default:         return IrrColors.cancelled
        }
    }
    
    // Check if context utilization data is available
    var hasContextData: Bool {
        return totalTokens > 0 && !modelName.isEmpty && pressureLevel != "unknown" && !pressureLevel.isEmpty
    }

    var formattedCost: String? {
        // Hide entirely when there's no session activity yet.
        guard totalTokens > 0 else { return nil }
        let cost = estimatedCostUSD ?? 0
        if cost > 0 {
            if cost < 0.01 { return "<$0.01" }
            if cost >= 100 { return String(format: "$%.0f", cost) }
            return String(format: "$%.2f", cost)
        }
        // Cost is zero with tokens flowing. We only render the explicit
        // "—" placeholder when the daemon has positively told us cost
        // can't be computed (model has no LiteLLM pricing entry, signaled
        // via contextWindowUnknown). For other adapters, returning nil
        // here keeps the historical "hide on transient zero-cost windows"
        // behavior — claudecode / codex / pi cost converges to a real
        // number within a turn, and we don't want a flicker through "—".
        if contextWindowUnknown == true { return "—" }
        return nil
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
    let tty: String?
    let kittyListenOn: String?
    let kittyWindowID: String?
    let kittyPID: Int?

    enum CodingKeys: String, CodingKey {
        case termProgram    = "term_program"
        case itermSessionID = "iterm_session_id"
        case termSessionID  = "term_session_id"
        case tmuxPane       = "tmux_pane"
        case tmuxSocket     = "tmux_socket"
        case tty            = "tty"
        case kittyListenOn  = "kitty_listen_on"
        case kittyWindowID  = "kitty_window_id"
        case kittyPID       = "kitty_pid"
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

        var color: Color {
            switch self {
            case .working: return IrrColors.working
            case .waiting: return IrrColors.waiting
            case .ready:   return IrrColors.ready
            }
        }

        /// Hex without leading `#`, for inline SVG `fill="#..."` markup.
        var hexColor: String {
            switch self {
            case .working: return IrrSVG.working
            case .waiting: return IrrSVG.waiting
            case .ready:   return IrrSVG.ready
            }
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

    /// Return a copy with a replacement children list. Used when the incremental
    /// WS patch path updates a single agent inside `apiGroups` — WS payloads
    /// don't carry `children`, so the patch has to reattach them.
    func withChildren(_ newChildren: [SessionState]?) -> SessionState {
        var copy = SessionState(
            id: id, state: state, model: model, cwd: cwd,
            transcriptPath: transcriptPath, gitBranch: gitBranch,
            projectName: projectName, firstSeen: firstSeen, updatedAt: updatedAt,
            eventCount: eventCount, lastEvent: lastEvent, metrics: metrics,
            pid: pid, parentSessionId: parentSessionId, subagents: subagents,
            adapter: adapter, daemonVersion: daemonVersion,
            role: role, roleIcon: roleIcon, roleDescription: roleDescription,
            workerName: workerName, workerID: workerID,
            children: newChildren, launcher: launcher
        )
        copy.duplicateIndex = duplicateIndex
        return copy
    }

    /// Return a copy with `state` replaced and `updatedAt` set to now. All
    /// other fields are preserved — including children, subagents, role, and
    /// adapter — which a field-by-field reconstruction would silently drop.
    func withState(_ newState: State) -> SessionState {
        var copy = SessionState(
            id: id, state: newState, model: model, cwd: cwd,
            transcriptPath: transcriptPath, gitBranch: gitBranch,
            projectName: projectName, firstSeen: firstSeen, updatedAt: Date(),
            eventCount: eventCount, lastEvent: lastEvent, metrics: metrics,
            pid: pid, parentSessionId: parentSessionId, subagents: subagents,
            adapter: adapter, daemonVersion: daemonVersion,
            role: role, roleIcon: roleIcon, roleDescription: roleDescription,
            workerName: workerName, workerID: workerID,
            children: children, launcher: launcher
        )
        copy.duplicateIndex = duplicateIndex
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
        var short = effectiveModel
        // Strip LiteLLM provider/routing prefix, e.g.
        // "openai/google/gemma-4-26b-a4b" → "gemma-4-26b-a4b"
        // "anthropic/claude-opus-4-7"     → "claude-opus-4-7"
        if let lastSlash = short.lastIndex(of: "/") {
            short = String(short[short.index(after: lastSlash)...])
        }
        short = short.replacingOccurrences(of: "claude-", with: "")
        // "sonnet-4-6" → "sonnet-4.6"
        if let range = short.range(of: #"-(\d+)$"#, options: .regularExpression) {
            short = short.replacingCharacters(in: range, with: "." + short[range].dropFirst())
        }
        return short
    }

    @MainActor
    var adapterName: String {
        let key = adapter ?? ""
        if let entry = AgentRegistry.byName[key], !entry.displayName.isEmpty {
            return entry.displayName
        }
        return key.isEmpty ? "Unknown" : key
    }

    /// SVG icon for the adapter, looked up from the registry the daemon
    /// publishes at GET /api/v1/agents. Falls back to a neutral generic icon
    /// when the registry has no entry for this adapter — e.g. before the
    /// first hydration completes, or when an adapter is rolled out by a
    /// daemon newer than this app build.
    @MainActor
    var adapterIcon: NSImage? {
        // NSApp can be nil before the app finishes launching (and is always
        // nil in unit-test contexts that don't bring up an NSApplication).
        // Default to the light variant in that case — it's the more common
        // ambient appearance and avoids an implicit-unwrap crash.
        let isDark = NSApp?.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        let svg: String
        if let entry = AgentRegistry.byName[adapter ?? ""] {
            svg = isDark ? entry.iconSVGDark : entry.iconSVGLight
        } else {
            svg = SessionState.genericAdapterSVG
        }
        guard let data = svg.data(using: .utf8),
              let img = NSImage(data: data) else { return nil }
        img.isTemplate = false
        img.size = NSSize(width: 14, height: 14)
        return img
    }

    // Neutral placeholder shown when the adapter registry has no entry for
    // this session's adapter (pre-hydration, or unknown adapter from a newer
    // daemon). Question mark in a circle reads as "unknown" so users don't
    // confuse a missing-branding icon with a real adapter; the gray
    // (#9CA3AF) is deliberately distinct from the cancelled-state gray
    // (#8E8E93) used elsewhere in the dashboard so the two never collide.
    // Per-adapter SVGs now live in their Go packages — see
    // core/adapters/inbound/agents/<name>/config.go.
    private static let genericAdapterSVG = """
    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
      <circle cx="50" cy="50" r="44" fill="none" stroke="#9CA3AF" stroke-width="8"/>
      <path d="M38 38 Q38 26 50 26 Q62 26 62 38 Q62 46 54 50 Q50 52 50 60" fill="none" stroke="#9CA3AF" stroke-width="8" stroke-linecap="round" stroke-linejoin="round"/>
      <circle cx="50" cy="74" r="5" fill="#9CA3AF"/>
    </svg>
    """
}
