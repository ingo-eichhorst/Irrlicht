import Foundation

/// Thin client for the daemon's task-eta activation endpoint (issue #558).
/// The daemon owns the consent state and the ~/.claude/CLAUDE.md write —
/// this client only flips and mirrors it, so the Settings toggle never
/// claims an install that didn't happen.
enum ActivationClient {
    /// The daemon's task-eta endpoint — nil in a test process, which makes
    /// `status()` and `set(enabled:)` answer nil ("daemon unreachable", which
    /// Settings already treats as "leave the toggle alone") without opening a
    /// socket. Rendering Settings with Advanced expanded runs `status()` from
    /// `.onAppear`, and a test doing so used to GET the developer's live
    /// daemon — one bug in the reconcile below away from a DELETE that
    /// uninstalls their CLAUDE.md block. `ActivationClientGuardTests` is the
    /// guard.
    static var endpoint: URL? { endpoint(underXCTest: AppHome.isRunningTests) }

    /// The rule behind `endpoint`, pure so it can be asserted without sending
    /// anything.
    static func endpoint(underXCTest: Bool) -> URL? {
        guard !underXCTest else { return nil }
        return URL(string: "\(DaemonEndpoint.httpBase)/api/v1/activation/task-eta")
    }

    private struct State: Codable {
        let taskEtaEnabled: Bool
        enum CodingKeys: String, CodingKey { case taskEtaEnabled = "task_eta_enabled" }
    }

    /// Current consent state, or nil when the daemon is unreachable. The
    /// caller MUST distinguish nil (unknown) from false (daemon says off) —
    /// treating unreachable as "off" silently uninstalls the managed block
    /// whenever Settings opens before the daemon is up (issue #558).
    static func status() async -> Bool? {
        guard let url = endpoint else { return nil }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = "GET"
        return await send(req)
    }

    /// Flip consent (POST = enable + install, DELETE = revoke + uninstall).
    /// Returns the daemon's resulting state, or nil when unreachable.
    static func set(enabled: Bool) async -> Bool? {
        guard let url = endpoint else { return nil }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = enabled ? "POST" : "DELETE"
        return await send(req)
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
