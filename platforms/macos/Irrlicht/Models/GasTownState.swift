import Foundation

/// Combined Gas Town snapshot from the irrlichtd /api/v1/gastown endpoint.
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
