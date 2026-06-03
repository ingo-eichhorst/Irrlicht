import Foundation

/// Thin client for the daemon's task-eta activation endpoint (issue #558).
/// The daemon owns the consent state and the ~/.claude/CLAUDE.md write —
/// this client only flips and mirrors it, so the Settings toggle never
/// claims an install that didn't happen.
enum ActivationClient {
    private static var endpoint: URL? {
        URL(string: "\(DaemonEndpoint.httpBase)/api/v1/activation/task-eta")
    }

    private struct State: Codable {
        let taskEtaEnabled: Bool
        enum CodingKeys: String, CodingKey { case taskEtaEnabled = "task_eta_enabled" }
    }

    /// Current consent state; false when the daemon is unreachable.
    static func status() async -> Bool {
        guard let url = endpoint else { return false }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = "GET"
        return await send(req) ?? false
    }

    /// Flip consent (POST = enable + install, DELETE = revoke + uninstall).
    /// Returns the daemon's resulting state — on failure it re-reads status
    /// so the caller can keep the toggle honest.
    static func set(enabled: Bool) async -> Bool {
        guard let url = endpoint else { return false }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = enabled ? "POST" : "DELETE"
        if let state = await send(req) { return state }
        return await status()
    }

    private static func send(_ req: URLRequest) async -> Bool? {
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else { return nil }
            return try JSONDecoder().decode(State.self, from: data).taskEtaEnabled
        } catch {
            return nil
        }
    }
}
