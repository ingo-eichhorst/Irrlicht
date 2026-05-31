import Foundation

/// The full orchestrator snapshot pushed by the daemon over the WebSocket as an
/// `orchestrator_state` frame. Mirrors `core/domain/orchestrator/state.go`.
/// Every collection is optional so older daemons (and partial states) decode
/// cleanly; the UI hides itself when `running` is false or the value is nil.
struct OrchestratorState: Decodable, Equatable {
    let adapter: String
    let running: Bool
    let root: String?
    let globalAgents: [GlobalAgent]?
    let codebases: [Codebase]?
    let workUnits: [WorkUnit]?
    let health: Health?

    enum CodingKeys: String, CodingKey {
        case adapter
        case running
        case root
        case globalAgents = "global_agents"
        case codebases
        case workUnits = "work_units"
        case health
    }

    /// Convoys are the work units the panel renders with progress.
    var convoys: [WorkUnit] { (workUnits ?? []).filter { $0.type == "convoy" } }

    struct GlobalAgent: Decodable, Equatable, Identifiable {
        let role: String
        let icon: String?
        let description: String?
        let sessionID: String?
        let state: String

        var id: String { sessionID ?? role }

        enum CodingKeys: String, CodingKey {
            case role, icon, description, state
            case sessionID = "session_id"
        }
    }

    struct Codebase: Decodable, Equatable, Identifiable {
        let name: String
        let repoURL: String?
        let status: String?
        let worktrees: [Worktree]?

        var id: String { name }

        enum CodingKeys: String, CodingKey {
            case name, status, worktrees
            case repoURL = "repo_url"
        }
    }

    struct Worktree: Decodable, Equatable {
        let path: String
        let branch: String?
        let isMain: Bool
        let workers: [Worker]?

        enum CodingKeys: String, CodingKey {
            case path, branch, workers
            case isMain = "is_main"
        }
    }

    struct Worker: Decodable, Equatable, Identifiable {
        let role: String
        let icon: String?
        let description: String?
        let name: String?
        let workerID: String?
        let sessionID: String?
        let state: String

        var id: String { sessionID ?? (workerID ?? role) }

        enum CodingKeys: String, CodingKey {
            case role, icon, description, name, state
            case workerID = "id"
            case sessionID = "session_id"
        }
    }

    struct WorkUnit: Decodable, Equatable, Identifiable {
        let id: String
        let type: String
        let name: String
        let source: String?
        let total: Int
        let done: Int

        var isDone: Bool { done >= total }
    }

    /// Daemon / watchdog liveness, mirroring `orchestrator.Health`.
    struct Health: Decodable, Equatable {
        let daemonRunning: Bool
        let pid: Int?
        let heartbeatCount: Int?
        let bootRunning: Bool?
        let bootDegraded: Bool?
        let sessionAlive: Bool?

        enum CodingKeys: String, CodingKey {
            case daemonRunning = "daemon_running"
            case pid
            case heartbeatCount = "heartbeat_count"
            case bootRunning = "boot_running"
            case bootDegraded = "boot_degraded"
            case sessionAlive = "session_alive"
        }
    }
}
