import Foundation

/// A backchannel event→action rule (issue #724), mirroring the daemon's
/// domain/backchannel.Rule JSON shape.
struct BackchannelRule: Codable, Identifiable, Equatable {
    var id: String
    var enabled: Bool
    var name: String?
    var trigger: Trigger
    var actions: [Action]
    var adapter: String?
    var cooldownSeconds: Int?

    struct Trigger: Codable, Equatable {
        var event: String
        var threshold: Double?
    }

    struct Action: Codable, Equatable {
        var kind: String
        /// Agent-agnostic preset id (issue #754); when set, the daemon
        /// translates it to the agent's command. nil means Custom (raw `data`).
        var preset: String? = nil
        var data: String?
    }

    enum CodingKeys: String, CodingKey {
        case id, enabled, name, trigger, actions, adapter
        case cooldownSeconds = "cooldown_seconds"
    }

    // Event identifiers, matching the daemon constants.
    static let eventWaiting = "waiting"
    static let eventReady = "ready"
    static let eventWorking = "working"
    static let eventContextPressure = "context_pressure"

    static let actionInput = "input"
    static let actionInterrupt = "interrupt"

    // Preset ids, matching the daemon's backchannel.Preset* constants.
    static let presetCompact = "compact"

    /// The presets the editor offers, id → label. Whether a given agent
    /// supports one is sourced from the daemon (AgentBranding.supportedPresets).
    static let presetCatalog: [(id: String, label: String)] = [
        (presetCompact, "Compact"),
    ]
}

/// Thin client for the daemon's backchannel rules endpoint (GET/PUT).
enum BackchannelRulesClient {
    private static var endpoint: URL? {
        URL(string: "\(DaemonEndpoint.httpBase)/api/v1/backchannel/rules")
    }

    private struct Body: Codable { let rules: [BackchannelRule] }

    /// Fetches the current rule set, or nil when the daemon is unreachable.
    static func fetch() async -> [BackchannelRule]? {
        guard let url = endpoint else { return nil }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = "GET"
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else { return nil }
            return try JSONDecoder().decode(Body.self, from: data).rules
        } catch {
            return nil
        }
    }

    /// Replaces the rule set. Returns true on success.
    @discardableResult
    static func save(_ rules: [BackchannelRule]) async -> Bool {
        guard let url = endpoint else { return false }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = "PUT"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        do {
            req.httpBody = try JSONEncoder().encode(Body(rules: rules))
            let (_, resp) = try await URLSession.shared.data(for: req)
            return (resp as? HTTPURLResponse)?.statusCode == 200
        } catch {
            return false
        }
    }
}
