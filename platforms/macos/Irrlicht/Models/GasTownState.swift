import Foundation

// MARK: - Enriched gastown_state model (per ADR)

/// Full Gas Town state as pushed via WebSocket or fetched via REST.
struct GasTownState: Codable {
    let type_: String?
    let running: Bool
    let gtRoot: String?
    let globalAgents: [GlobalAgent]?
    let codebases: [GasTownCodebase]?
    let workUnits: [WorkUnit]?
    let updatedAt: Date?

    enum CodingKeys: String, CodingKey {
        case type_ = "type"
        case running
        case gtRoot = "gt_root"
        case globalAgents = "global_agents"
        case codebases
        case workUnits = "work_units"
        case updatedAt = "updated_at"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        type_ = try container.decodeIfPresent(String.self, forKey: .type_)
        running = try container.decode(Bool.self, forKey: .running)
        gtRoot = try container.decodeIfPresent(String.self, forKey: .gtRoot)
        globalAgents = try container.decodeIfPresent([GlobalAgent].self, forKey: .globalAgents)
        codebases = try container.decodeIfPresent([GasTownCodebase].self, forKey: .codebases)
        workUnits = try container.decodeIfPresent([WorkUnit].self, forKey: .workUnits)

        if let dateString = try? container.decode(String.self, forKey: .updatedAt) {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            updatedAt = formatter.date(from: dateString)
                ?? ISO8601DateFormatter().date(from: dateString)
        } else {
            updatedAt = nil
        }
    }

    init(running: Bool, gtRoot: String? = nil, globalAgents: [GlobalAgent]? = nil,
         codebases: [GasTownCodebase]? = nil, workUnits: [WorkUnit]? = nil) {
        self.type_ = "gastown_state"
        self.running = running
        self.gtRoot = gtRoot
        self.globalAgents = globalAgents
        self.codebases = codebases
        self.workUnits = workUnits
        self.updatedAt = Date()
    }

    var safeGlobalAgents: [GlobalAgent] { globalAgents ?? [] }
    var safeCodebases: [GasTownCodebase] { codebases ?? [] }
    var safeWorkUnits: [WorkUnit] { workUnits ?? [] }

    /// Active convoys (work units of type "convoy" that aren't fully done).
    var activeConvoys: [WorkUnit] {
        safeWorkUnits.filter { $0.type_ == "convoy" && $0.done < $0.total }
    }

    /// Completed convoys.
    var completedConvoys: [WorkUnit] {
        safeWorkUnits.filter { $0.type_ == "convoy" && $0.done >= $0.total }
    }

    /// All convoys.
    var convoys: [WorkUnit] {
        safeWorkUnits.filter { $0.type_ == "convoy" }
    }

    /// Active rig count (rigs with at least one working agent).
    var activeRigCount: Int {
        safeCodebases.filter { cb in
            cb.safeWorktrees.contains { wt in
                wt.safeAgents.contains { $0.state == "working" }
            }
        }.count
    }
}

// MARK: - Global Agent

struct GlobalAgent: Codable, Identifiable {
    let role: String
    let sessionId: String?
    let state: String

    var id: String { role }

    enum CodingKeys: String, CodingKey {
        case role
        case sessionId = "session_id"
        case state
    }

    var isMayor: Bool { role == "mayor" }
    var isDeacon: Bool { role == "deacon" }
    var isActive: Bool { state == "working" || state == "waiting" }

    var displayIcon: String {
        switch role {
        case "mayor": return "crown.fill"
        case "deacon": return "person.fill.checkmark"
        default: return "person.fill"
        }
    }

    var displayEmoji: String {
        switch role {
        case "mayor": return "🎩"
        case "deacon": return "📋"
        default: return "👤"
        }
    }

    var stateColor: String {
        switch state {
        case "working": return "#8B5CF6"
        case "waiting": return "#FF9500"
        case "ready": return "#34C759"
        default: return "#8E8E93"
        }
    }

    var stateGlyph: String {
        switch state {
        case "working": return "hammer.fill"
        case "waiting": return "hourglass"
        case "ready": return "checkmark.circle.fill"
        default: return "minus.circle"
        }
    }
}

// MARK: - Codebase (Rig)

struct GasTownCodebase: Codable, Identifiable {
    let rig: String
    let repoUrl: String?
    let status: String?
    let worktrees: [GasTownWorktree]?

    var id: String { rig }

    enum CodingKeys: String, CodingKey {
        case rig
        case repoUrl = "repo_url"
        case status
        case worktrees
    }

    var safeWorktrees: [GasTownWorktree] { worktrees ?? [] }

    var isOperational: Bool { status == "operational" }

    /// Main worktree (witness + refinery + crew).
    var mainWorktree: GasTownWorktree? {
        safeWorktrees.first { $0.isMain }
    }

    /// Polecat worktrees.
    var polecatWorktrees: [GasTownWorktree] {
        safeWorktrees.filter { !$0.isMain }
    }

    /// All agents across all worktrees.
    var allAgents: [GasTownAgent] {
        safeWorktrees.flatMap { $0.safeAgents }
    }

    /// Witness agent from main worktree.
    var witness: GasTownAgent? {
        mainWorktree?.safeAgents.first { $0.role == "witness" }
    }

    /// Refinery agent from main worktree.
    var refinery: GasTownAgent? {
        mainWorktree?.safeAgents.first { $0.role == "refinery" }
    }

    /// All polecat agents.
    var polecats: [GasTownAgent] {
        polecatWorktrees.flatMap { $0.safeAgents }
    }

    /// Crew agents from main worktree.
    var crewAgents: [GasTownAgent] {
        mainWorktree?.safeAgents.filter { $0.role == "crew" } ?? []
    }

    /// Total agent count.
    var agentCount: Int {
        allAgents.count
    }
}

// MARK: - Worktree

struct GasTownWorktree: Codable, Identifiable {
    let path: String
    let branch: String?
    let isMain: Bool
    let agents: [GasTownAgent]?

    var id: String { path }

    enum CodingKeys: String, CodingKey {
        case path, branch, agents
        case isMain = "is_main"
    }

    var safeAgents: [GasTownAgent] { agents ?? [] }
}

// MARK: - Agent

struct GasTownAgent: Codable, Identifiable {
    let role: String
    let name: String?
    let beadId: String?
    let sessionId: String?
    let state: String

    var id: String {
        if let name = name {
            return "\(role)/\(name)"
        }
        return role
    }

    enum CodingKeys: String, CodingKey {
        case role, name, state
        case beadId = "bead_id"
        case sessionId = "session_id"
    }

    var isPolecat: Bool { role == "polecat" }
    var isWitness: Bool { role == "witness" }
    var isRefinery: Bool { role == "refinery" }
    var isCrew: Bool { role == "crew" }
    var isActive: Bool { state == "working" || state == "waiting" }

    var displayIcon: String {
        switch role {
        case "witness": return "eye.fill"
        case "refinery": return "gearshape.2.fill"
        case "polecat": return "wrench.and.screwdriver.fill"
        case "crew": return "person.crop.circle.fill"
        default: return "person.fill"
        }
    }

    var displayEmoji: String {
        switch role {
        case "witness": return "🦉"
        case "refinery": return "🏭"
        case "polecat": return "👷"
        case "crew": return "🧑‍💻"
        default: return "👤"
        }
    }

    var stateColor: String {
        switch state {
        case "working": return "#8B5CF6"
        case "waiting": return "#FF9500"
        case "ready", "running": return "#34C759"
        default: return "#8E8E93"
        }
    }

    var stateGlyph: String {
        switch state {
        case "working": return "hammer.fill"
        case "waiting": return "hourglass"
        case "ready", "running": return "checkmark.circle.fill"
        default: return "minus.circle"
        }
    }

    /// Display label: name or role.
    var displayLabel: String {
        name ?? role.capitalized
    }
}

// MARK: - Work Unit (Convoy / Task List)

struct WorkUnit: Codable, Identifiable {
    let id: String
    let type_: String
    let name: String
    let source: String
    let sessionId: String?
    let total: Int
    let done: Int

    enum CodingKeys: String, CodingKey {
        case id
        case type_ = "type"
        case name, source, total, done
        case sessionId = "session_id"
    }

    var isConvoy: Bool { type_ == "convoy" }
    var isTaskList: Bool { type_ == "task_list" }
    var isComplete: Bool { done >= total }
    var progress: Double { total > 0 ? Double(done) / Double(total) : 0 }

    /// Generate dot-bar representation (max 7 dots).
    var dotBar: String {
        let maxDots = 7
        let totalDots = min(total, maxDots)
        guard totalDots > 0 else { return "" }

        let filledDots: Int
        if total <= maxDots {
            filledDots = done
        } else {
            // Proportional: round to nearest dot.
            filledDots = Int(round(Double(done) / Double(total) * Double(maxDots)))
        }

        let filled = String(repeating: "●", count: min(filledDots, totalDots))
        let empty = String(repeating: "○", count: max(totalDots - filledDots, 0))
        return filled + empty
    }

    /// Fraction string (e.g. "3 / 5").
    var fractionString: String {
        "\(done) / \(total)"
    }

    /// Color based on state.
    var dotColor: String {
        if isComplete { return "#34C759" }  // green
        return "#8B5CF6"  // purple for active
    }
}

// MARK: - Legacy Compatibility

/// Legacy snapshot format (kept for backward compat during transition).
struct GasTownSnapshot: Codable {
    let detected: Bool
    let daemon: DaemonState?
    let rigs: [RigState]?
    let polecats: [PolecatState]?
    let updatedAt: Date?

    enum CodingKeys: String, CodingKey {
        case detected, daemon, rigs, polecats
        case updatedAt = "updated_at"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        detected = try container.decode(Bool.self, forKey: .detected)
        daemon = try container.decodeIfPresent(DaemonState.self, forKey: .daemon)
        rigs = try container.decodeIfPresent([RigState].self, forKey: .rigs)
        polecats = try container.decodeIfPresent([PolecatState].self, forKey: .polecats)

        if let dateString = try? container.decode(String.self, forKey: .updatedAt) {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            updatedAt = formatter.date(from: dateString)
                ?? ISO8601DateFormatter().date(from: dateString)
        } else {
            updatedAt = nil
        }
    }
}

struct DaemonState: Codable {
    let running: Bool
    let pid: Int
    let startedAt: String?
    let lastHeartbeat: String?
    let heartbeatCount: Int?

    enum CodingKeys: String, CodingKey {
        case running, pid
        case startedAt = "started_at"
        case lastHeartbeat = "last_heartbeat"
        case heartbeatCount = "heartbeat_count"
    }
}

struct RigState: Codable, Identifiable {
    let name: String
    let beadsPrefix: String?
    let status: String
    let witness: String
    let refinery: String
    let polecats: Int
    let crew: Int

    var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, status, witness, refinery, polecats, crew
        case beadsPrefix = "beads_prefix"
    }

    var isOperational: Bool { status == "operational" }
    var witnessRunning: Bool { witness == "running" }
    var refineryRunning: Bool { refinery == "running" }
    var agentCount: Int { polecats + crew }
}

struct PolecatState: Codable, Identifiable {
    let rig: String
    let name: String
    let state: String
    let issue: String?
    let sessionRunning: Bool?

    var id: String { "\(rig)/\(name)" }

    enum CodingKeys: String, CodingKey {
        case rig, name, state, issue
        case sessionRunning = "session_running"
    }

    var isWorking: Bool { state == "working" }
}
